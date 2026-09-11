package agentruntime

import (
	"errors"
	"testing"
)

type eventTestRun struct {
	events   []Event
	index    int
	canceled bool
	closed   bool
}

func (r *eventTestRun) Next() Event {
	if r.index >= len(r.events) {
		return Event{Kind: EventFailed, Err: errors.New("test run exhausted")}
	}
	event := r.events[r.index]
	r.index++
	return event
}

func (r *eventTestRun) Cancel() { r.canceled = true }

func (r *eventTestRun) Close() Event {
	r.closed = true
	return r.events[len(r.events)-1]
}

func TestIsTerminalUsesRunEventVocabulary(t *testing.T) {
	for _, kind := range []EventKind{EventCompleted, EventCanceled, EventFailed} {
		if !IsTerminal(kind) || !(Event{Kind: kind}).IsTerminal() {
			t.Fatalf("%q was not terminal", kind)
		}
	}
	for _, kind := range []EventKind{EventTextDelta, EventReasoningDelta, EventToolStarted, EventCompressing} {
		if IsTerminal(kind) || (Event{Kind: kind}).IsTerminal() {
			t.Fatalf("%q was unexpectedly terminal", kind)
		}
	}
}

func TestConsumeDeliversAllEventsThroughTerminal(t *testing.T) {
	run := &eventTestRun{events: []Event{
		{Kind: EventTextDelta, Text: "hello"},
		{Kind: EventToolStarted, Tool: "calculator", CallID: "call-1", Arguments: `{"a":2}`},
		{Kind: EventCompleted, Text: "hello"},
	}}
	var seen []EventKind
	terminal, err := Consume(run, func(event Event) error {
		seen = append(seen, event.Kind)
		return nil
	})
	if err != nil || terminal.Kind != EventCompleted {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	if len(seen) != 3 || seen[1] != EventToolStarted || run.canceled || run.closed {
		t.Fatalf("seen=%v canceled=%v closed=%v", seen, run.canceled, run.closed)
	}
}

func TestConsumeCancelsAndClosesWhenSinkFails(t *testing.T) {
	failure := errors.New("client disconnected")
	run := &eventTestRun{events: []Event{{Kind: EventTextDelta, Text: "partial"}, {Kind: EventCompleted}}}
	terminal, err := Consume(run, func(event Event) error {
		if event.Kind == EventTextDelta {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || terminal.Kind != EventCompleted || !run.canceled || !run.closed {
		t.Fatalf("terminal=%+v err=%v canceled=%v closed=%v", terminal, err, run.canceled, run.closed)
	}
}
