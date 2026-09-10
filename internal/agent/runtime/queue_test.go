package agentruntime

import (
	"context"
	"strconv"
	"testing"
	"time"

	"easygo-agent/internal/conversation"
	"easygo-agent/internal/testutil"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestQueueManagerSubmitsAndCommitsClaimedRuns(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewQueueManager(ctx, store, streamAgent([]*schema.AgenticMessage{testutil.Text("answer")}), QueueConfig{
		MaxWorkers:   2,
		PollInterval: time.Millisecond,
		LeaseTTL:     time.Second,
	})
	defer manager.Close()

	record, _, err := manager.Submit(ctx, "queue-user", session.ID, "question", "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != conversation.RunQueued || record.Position != 1 {
		t.Fatalf("unexpected submit record: %+v", record)
	}
	subscription, err := manager.Subscribe(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var terminal Event
	for event := range subscription.Events() {
		if event.IsTerminal() {
			terminal = event
		}
	}
	if terminal.Kind != EventCompleted || terminal.RunID != record.ID || terminal.Text != "answer" {
		t.Fatalf("terminal event: %+v", terminal)
	}
	final, err := manager.Get(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != conversation.RunCompleted || final.TurnID == 0 {
		t.Fatalf("final run: %+v", final)
	}
	history, err := store.History(ctx, "queue-user", session.ID, 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestQueueManagerSameSessionIsFIFO(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewQueueManager(ctx, store, streamAgent([]*schema.AgenticMessage{testutil.Text("ok")}), QueueConfig{MaxWorkers: 4, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	first, _, err := manager.Submit(ctx, "queue-user", session.ID, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := manager.Submit(ctx, "queue-user", session.ID, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, getErr := manager.Get(ctx, "queue-user", session.ID, second.ID)
		if getErr == nil && isRunTerminal(got.Status) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	history, err := store.History(ctx, "queue-user", session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected two turns, got %+v", history)
	}
	if history[0].Messages[0].ContentBlocks[0].UserInputText.Text != "first" || history[1].Messages[0].ContentBlocks[0].UserInputText.Text != "second" {
		t.Fatalf("history order does not follow enqueue order: %+v %+v", first, second)
	}
}

func TestQueueManagerRunningCancelAuditsPartialOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	agent := &scriptedAgent{run: func(runCtx context.Context, _ *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iterator, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() {
			defer generator.Close()
			generator.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{IsStreaming: true, MessageStream: reader}}})
			writer.Send(testutil.Text("partial"), nil)
			<-runCtx.Done()
			writer.Close()
		}()
		return iterator
	}}
	manager := NewQueueManager(ctx, store, agent, QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	defer manager.Close()
	record, _, err := manager.Submit(ctx, "queue-user", session.ID, "cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := manager.Get(ctx, "queue-user", session.ID, record.ID)
		if getErr == nil && current.Status == conversation.RunRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = manager.Cancel(ctx, "queue-user", session.ID, record.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := manager.Get(ctx, "queue-user", session.ID, record.ID)
		if getErr == nil && current.Status == conversation.RunCanceled {
			turns, historyErr := store.History(ctx, "queue-user", session.ID, 0, 10)
			if historyErr == nil && len(turns) == 1 {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	current, _ := manager.Get(ctx, "queue-user", session.ID, record.ID)
	if current.Status != conversation.RunCanceled {
		t.Fatalf("cancel did not finish: %+v", current)
	}
	lease, err := store.Begin(ctx, "queue-user", session.ID)
	if err != nil {
		t.Fatalf("session remained locked: %v", err)
	}
	lease.Close()
}

func TestQueueSubscriptionPreservesBurstBeforeTerminal(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	manager := &queueManager{
		store:       store,
		ctx:         managerCtx,
		cancel:      cancel,
		wake:        make(chan struct{}, 1),
		active:      make(map[string]*activeRun),
		subscribers: make(map[subscriptionKey]map[*runSubscription]struct{}),
	}
	defer manager.Close()
	record, err := store.Enqueue(ctx, "queue-user", session.ID, "burst", "")
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := manager.Subscribe(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		manager.publish(Event{Kind: EventTextDelta, RunID: record.ID, Text: strconv.Itoa(i)})
	}
	manager.publish(Event{Kind: EventCompleted, RunID: record.ID, Status: conversation.RunCompleted})
	count := 0
	for event := range subscription.Events() {
		if event.Kind == EventTextDelta {
			count++
		}
	}
	if count != 128 {
		t.Fatalf("subscription dropped burst events: got %d", count)
	}
}

func TestQueueCancelPublishesQueuedTerminalEvent(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &queueManager{
		store:       store,
		ctx:         managerCtx,
		cancel:      cancel,
		wake:        make(chan struct{}, 1),
		active:      make(map[string]*activeRun),
		subscribers: make(map[subscriptionKey]map[*runSubscription]struct{}),
	}
	t.Cleanup(func() { _ = manager.Close() })
	record, _, err := manager.Submit(ctx, "queue-user", session.ID, "cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := manager.Subscribe(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err = manager.Cancel(ctx, "queue-user", session.ID, record.ID); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range subscription.Events() {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Kind != EventQueued || events[1].Kind != EventCanceled {
		t.Fatalf("queued cancellation events=%+v", events)
	}
}

func TestQueueManagerCloseForceStopsUndrainedTerminalSubscription(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &queueManager{
		store:       store,
		ctx:         managerCtx,
		cancel:      cancel,
		wake:        make(chan struct{}, 1),
		active:      make(map[string]*activeRun),
		subscribers: make(map[subscriptionKey]map[*runSubscription]struct{}),
	}
	record, err := store.Enqueue(ctx, "queue-user", session.ID, "undrained", "")
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := manager.Subscribe(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Do not read the channel before shutdown. A terminal publication must not
	// leave its dispatcher blocked forever when the application closes.
	manager.publish(Event{Kind: EventCompleted, RunID: record.ID, Status: conversation.RunCompleted})
	closed := make(chan struct{})
	go func() {
		_ = manager.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("manager close blocked on an undrained subscription")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-subscription.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription channel did not close")
		}
	}
}

func TestQueueSubscriptionPollsDurableTerminalAcrossManagers(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Enqueue(ctx, "queue-user", session.ID, "cross-process", "")
	if err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &queueManager{
		store:       store,
		ctx:         managerCtx,
		cancel:      cancel,
		cfg:         QueueConfig{PollInterval: 5 * time.Millisecond},
		wake:        make(chan struct{}, 1),
		active:      make(map[string]*activeRun),
		subscribers: make(map[subscriptionKey]map[*runSubscription]struct{}),
	}
	t.Cleanup(func() { _ = manager.Close() })
	subscription, err := manager.Subscribe(ctx, "queue-user", session.ID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	lease, err := store.ClaimNext(ctx, "other-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.CommitRun(ctx, nil, conversation.RunCompleted, []*schema.AgenticMessage{testutil.Text("done")}, "done", ""); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				t.Fatal("subscription closed before durable terminal state")
			}
			if event.Kind == EventCompleted {
				if event.Text != "done" || event.Status != conversation.RunCompleted {
					t.Fatalf("cross-process terminal event=%+v", event)
				}
				return
			}
		case <-deadline:
			t.Fatal("durable terminal state was not observed by polling")
		}
	}
}

func TestQueueManagerCloseCancelsRunningAndPreservesQueued(t *testing.T) {
	ctx := context.Background()
	store := conversation.NewMemory()
	session, err := store.Create(ctx, "queue-user")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	agent := &scriptedAgent{run: func(runCtx context.Context, _ *adk.TypedAgentInput[*schema.AgenticMessage]) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
		iterator, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
		reader, writer := schema.Pipe[*schema.AgenticMessage](1)
		go func() {
			defer generator.Close()
			defer writer.Close()
			generator.Send(&adk.TypedAgentEvent[*schema.AgenticMessage]{Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{IsStreaming: true, MessageStream: reader}}})
			close(entered)
			writer.Send(testutil.Text("partial"), nil)
			<-runCtx.Done()
		}()
		return iterator
	}}
	manager := NewQueueManager(ctx, store, agent, QueueConfig{MaxWorkers: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second})
	first, _, err := manager.Submit(ctx, "queue-user", session.ID, "running", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		_ = manager.Close()
		t.Fatal("worker did not start")
	}
	second, _, err := manager.Submit(ctx, "queue-user", session.ID, "queued", "")
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		_ = manager.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("manager close did not wait for running cancellation")
	}
	running, err := store.GetRun(ctx, "queue-user", session.ID, first.ID)
	if err != nil || running.Status != conversation.RunCanceled {
		t.Fatalf("running status after close=%+v err=%v", running, err)
	}
	queued, err := store.GetRun(ctx, "queue-user", session.ID, second.ID)
	if err != nil || queued.Status != conversation.RunQueued {
		t.Fatalf("queued status after close=%+v err=%v", queued, err)
	}
}
