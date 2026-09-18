package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"
	"go.uber.org/zap"
)

type Service struct {
	Store         Store
	Engine        *Engine
	Conversations conversation.QueueStore
	mu            sync.Mutex
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func (s *Service) Spawn(ctx context.Context, o Owner, b Brief) (Task, error) {
	if _, ok := s.Engine.Config.Roles[b.Role]; !ok {
		return Task{}, fmt.Errorf("%w: unknown role %s", ErrInvalid, b.Role)
	}
	parent, err := s.Conversations.GetRun(ctx, o.User, o.Session, o.Run)
	if err != nil {
		return Task{}, err
	}
	if parent.Status != conversation.RunRunning || parent.CancelRequested || parent.Source != "" {
		return Task{}, fmt.Errorf("%w: delegation requires an active user run", ErrInvalid)
	}
	t, err := s.Store.Create(ctx, o, b)
	if err != nil {
		return Task{}, err
	}
	// Close the cancellation-vs-submit race; workers also recheck the durable parent.
	parent, err = s.Conversations.GetRun(ctx, o.User, o.Session, o.Run)
	if err != nil || parent.CancelRequested {
		_, _ = s.Store.Cancel(context.WithoutCancel(ctx), o, t.ID)
		if err == nil {
			err = context.Canceled
		}
		return Task{}, err
	}
	return t, nil
}
func (s *Service) Summary(ctx context.Context, user, session string) (string, error) {
	tasks, err := s.Store.List(ctx, Owner{User: user, Session: session})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range tasks {
		if t.Status == Completed || t.Status == Canceled {
			continue
		}
		fmt.Fprintf(&b, "Task %s v%d [%s]: %s; progress: %s; blocker: %s\n", t.ID, t.Version, t.Status, t.Brief.Goal, t.Progress, t.Reason)
	}
	if b.Len() == 0 {
		return "", nil
	}
	return "Unfinished background tasks (query get_task for full evidence):\n" + b.String(), nil
}
func (s *Service) CancelChildren(ctx context.Context, user, session, run string) error {
	tasks, err := s.Store.List(ctx, Owner{User: user, Session: session})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ParentRun == run && t.Status != Completed && t.Status != Canceled {
			if _, err = s.Store.Cancel(ctx, t.Owner, t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) Start(parent context.Context) error {
	if err := s.Engine.Validate(parent); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("task workers already started")
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	for i := 0; i < s.Engine.Config.Workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	s.wg.Add(1)
	go s.notifications(ctx)
	return nil
}
func (s *Service) Close() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.wg.Wait()
	}
}
func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for ctx.Err() == nil {
		t, err := s.Store.Claim(ctx, s.Engine.Config.LeaseTTL)
		if err == nil {
			s.execute(ctx, t)
			continue
		}
		if !errors.Is(err, ErrNoTask) && ctx.Err() == nil {
			logger.Error("task claim failed", zap.Error(err))
		}
		if !pause(ctx, s.Engine.Config.PollInterval) {
			return
		}
	}
}
func (s *Service) execute(parent context.Context, t Task) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stopped := make(chan struct{})
	done := make(chan struct{})
	defer func() {
		close(stopped)
		<-done
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = s.Store.Release(releaseCtx, t.ID, t.Token)
	}()
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.Engine.Config.LeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				hbCtx, hbCancel := context.WithTimeout(ctx, s.Engine.Config.LeaseTTL/3)
				var err error
				if t.ParentRun != "" {
					var parentRun conversation.RunRecord
					parentRun, err = s.Conversations.GetRun(hbCtx, t.Owner.User, t.Owner.Session, t.ParentRun)
					if err == nil && parentRun.CancelRequested {
						_, err = s.Store.Cancel(hbCtx, t.Owner, t.ID)
						cancel()
					}
				}

				if err == nil {
					err = s.Store.Heartbeat(hbCtx, t.ID, t.Token, s.Engine.Config.LeaseTTL)
				}
				hbCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	if t.ParentRun != "" {
		record, err := s.Conversations.GetRun(ctx, t.Owner.User, t.Owner.Session, t.ParentRun)
		if err != nil {
			return
		}
		if record.CancelRequested {
			_, _ = s.Store.Cancel(ctx, t.Owner, t.ID)
			return
		}
	}

	if err := s.Engine.Execute(ctx, t); err != nil && ctx.Err() == nil && !errors.Is(err, ErrLeaseLost) {
		logger.Error("task execution interrupted", zap.String("task_id", t.ID), zap.Error(err))
	}
}
func (s *Service) notifications(ctx context.Context) {
	defer s.wg.Done()
	for ctx.Err() == nil {
		if err := s.Dispatch(ctx); err != nil && ctx.Err() == nil {
			logger.Error("task notification failed", zap.Error(err))
		}
		if !pause(ctx, s.Engine.Config.PollInterval) {
			return
		}
	}
}

// Dispatch is repeatable at every crash boundary. EnqueueInternal deduplicates
// by notification ID; summary completion atomically acknowledges that ID.
func (s *Service) Dispatch(ctx context.Context) error {
	sink, ok := s.Conversations.(conversation.NotificationStore)
	if !ok {
		return errors.New("conversation store does not support internal notifications")
	}
	pending, err := s.Store.Pending(ctx)
	if err != nil {
		return err
	}
	for _, n := range pending {
		if _, err = sink.EnqueueInternal(ctx, n.Owner.User, n.Owner.Session, n.Content, n.ID); err != nil {
			return err
		}
		if err = s.Store.MarkEnqueued(ctx, n.ID); err != nil {
			return err
		}
	}
	return nil
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
