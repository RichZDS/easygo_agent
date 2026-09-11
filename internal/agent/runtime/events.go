package agentruntime

// IsTerminal reports whether an event ends a run.
func IsTerminal(kind EventKind) bool {
	return kind == EventCompleted || kind == EventCanceled || kind == EventFailed
}

// IsTerminal reports whether this event ends a run.
func (event Event) IsTerminal() bool {
	return IsTerminal(event.Kind)
}

// Consume reads a run until its terminal event and invokes sink for every
// event, including the terminal event. If sink fails, the run is canceled and
// closed so persistence and lease cleanup still happen before returning.
func Consume(run Run, sink func(Event) error) (Event, error) {
	if run == nil {
		return Event{Kind: EventFailed, Err: ErrAgentUnavailable}, ErrAgentUnavailable
	}
	for {
		event := run.Next()
		if sink != nil {
			if err := sink(event); err != nil {
				run.Cancel()
				return run.Close(), err
			}
		}
		if event.IsTerminal() {
			return event, nil
		}
	}
}
