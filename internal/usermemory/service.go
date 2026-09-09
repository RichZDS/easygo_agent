package usermemory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/logger"

	"go.uber.org/zap"
)

// Service is the daily consolidation coordinator. A checkpoint advances only
// after profile materialization, so transient model or filesystem failures are
// retried rather than silently losing conversations.
type Service struct {
	store       conversation.MemoryStore
	transcripts conversation.TranscriptStore
	agent       Agent
	writer      ProfileWriter
	config      Config
	now         func() time.Time
}

func NewService(store conversation.MemoryStore, transcripts conversation.TranscriptStore, agent Agent, writer ProfileWriter, cfg Config) (*Service, error) {
	if store == nil || transcripts == nil || agent == nil || writer == nil {
		return nil, errors.New("memory service requires stores, an agent, and a profile writer")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Service{store: store, transcripts: transcripts, agent: agent, writer: writer, config: cfg, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) ConsolidateAll(ctx context.Context) error {
	through := s.now().UTC()
	// Start from every known user, not just today's active users. A user whose
	// prior 03:00 run failed must be retried even if they have not sent a new
	// message today; the per-user checkpoint makes no-op checks cheap.
	users, err := s.transcripts.UsersWithTranscript(ctx, time.Time{}, through)
	if err != nil {
		return err
	}
	for _, user := range users {
		if err = s.ConsolidateUser(ctx, user, through); err != nil {
			return fmt.Errorf("consolidate memory for %s: %w", user, err)
		}
	}
	return nil
}

func (s *Service) ConsolidateUser(ctx context.Context, user string, through time.Time) error {
	lease, acquired, err := s.store.TryAcquireMemoryJob(ctx, user)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer lease.Close()
	after, ok, err := s.store.MemoryCheckpoint(ctx, user)
	if err != nil {
		return err
	}
	if !ok {
		after = through.Add(-24 * time.Hour)
	}
	turns, err := s.transcripts.Transcript(ctx, user, after, through)
	if err != nil {
		return err
	}
	if len(turns) == 0 {
		return s.store.SetMemoryCheckpoint(ctx, user, through)
	}
	candidates := []Candidate{}
	for start := 0; start < len(turns); start += s.config.BatchTurns {
		end := min(start+s.config.BatchTurns, len(turns))
		batch := turns[start:end]
		extracted, extractErr := s.agent.Extract(ctx, user, batch)
		if extractErr != nil {
			return fmt.Errorf("extract batch %d: %w", start/s.config.BatchTurns+1, extractErr)
		}
		candidates = append(candidates, validateCandidates(extracted, batch)...)
	}
	if len(candidates) == 0 {
		return s.store.SetMemoryCheckpoint(ctx, user, through)
	}
	top := rankCandidates(candidates, s.config.TopK)
	existing, err := s.store.ActiveProfile(ctx, user)
	if err != nil {
		return err
	}
	drafts, err := s.agent.Reconcile(ctx, user, existing, top)
	if err != nil {
		return err
	}
	if len(drafts) > conversation.MaxProfileMemories {
		return conversation.ErrTooManyProfileMemories
	}
	for _, draft := range drafts {
		if err := validateReconciledDraft(draft, existing, top); err != nil {
			return err
		}
	}
	profile, err := s.store.ReplaceProfile(ctx, user, drafts)
	if err != nil {
		return err
	}
	if err = s.writer.Write(ctx, user, profile); err != nil {
		return err
	}
	return s.store.SetMemoryCheckpoint(ctx, user, through)
}

func validateCandidates(candidates []Candidate, batch []conversation.TranscriptTurn) []Candidate {
	sessions := map[string]bool{}
	turns := map[int64]bool{}
	for _, turn := range batch {
		sessions[turn.SessionID] = true
		turns[turn.ID] = true
	}
	result := []Candidate{}
	for _, candidate := range candidates {
		if !candidate.Kind.Valid() || candidate.Content == "" || len([]rune(candidate.Content)) > 4000 || candidate.Importance < 0 || candidate.Importance > 1 || candidate.Confidence < 0 || candidate.Confidence > 1 || looksSensitive(candidate.Content) {
			continue
		}
		candidate.SourceSessions = filter(candidate.SourceSessions, sessions)
		candidate.SourceTurnIDs = filterInt64(candidate.SourceTurnIDs, turns)
		if len(candidate.SourceSessions) == 0 || len(candidate.SourceTurnIDs) == 0 {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func validateReconciledDraft(draft conversation.MemoryDraft, existing []conversation.LongTermMemory, candidates []Candidate) error {
	if !draft.Kind.Valid() || draft.Content == "" || len([]rune(draft.Content)) > 4000 || draft.Importance < 0 || draft.Importance > 1 || draft.Confidence < 0 || draft.Confidence > 1 || looksSensitive(draft.Content) {
		return conversation.ErrInvalidMemory
	}
	allowedSessions := map[string]bool{}
	allowedTurns := map[int64]bool{}
	for _, candidate := range candidates {
		for _, source := range candidate.SourceSessions {
			allowedSessions[source] = true
		}
		for _, source := range candidate.SourceTurnIDs {
			allowedTurns[source] = true
		}
	}
	if draft.ID != "" {
		found := false
		for _, memory := range existing {
			if memory.ID == draft.ID {
				found = true
				for _, source := range memory.SourceSessions {
					allowedSessions[source] = true
				}
				for _, source := range memory.SourceTurnIDs {
					allowedTurns[source] = true
				}
				break
			}
		}
		if !found {
			return conversation.ErrInvalidMemory
		}
	}
	if len(draft.SourceSessions) == 0 || len(draft.SourceTurnIDs) == 0 {
		return conversation.ErrInvalidMemory
	}
	for _, source := range draft.SourceSessions {
		if !allowedSessions[source] {
			return conversation.ErrInvalidMemory
		}
	}
	for _, source := range draft.SourceTurnIDs {
		if !allowedTurns[source] {
			return conversation.ErrInvalidMemory
		}
	}
	return nil
}

var sensitiveMemoryPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(api[_ -]?key|password|secret|access[_ -]?token)\b\s*[:=]|\bsk-[a-z0-9]{12,})`)

func looksSensitive(content string) bool { return sensitiveMemoryPattern.MatchString(content) }

func filter(values []string, allowed map[string]bool) []string {
	result := []string{}
	for _, value := range values {
		if allowed[value] {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
func filterInt64(values []int64, allowed map[int64]bool) []int64 {
	result := []int64{}
	for _, value := range values {
		if allowed[value] {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// StartScheduler begins the daily 03:00-style trigger and returns immediately.
// Only the long-running TUI/Gateway process should call it; CLI one-shot runs
// finish before a scheduled job could be useful.
func (s *Service) StartScheduler(ctx context.Context) error {
	hour, minute, err := parseDailyAt(s.config.DailyAt)
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(s.config.Timezone)
	if err != nil {
		return fmt.Errorf("load memory timezone: %w", err)
	}
	go func() {
		for {
			next := nextDailyRun(time.Now(), location, hour, minute)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				if err := s.ConsolidateAll(ctx); err != nil && ctx.Err() == nil {
					logger.Error("daily user-memory consolidation failed", zap.Error(err))
				}
			}
		}
	}()
	return nil
}
