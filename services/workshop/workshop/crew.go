package workshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// CrewChannel is bound by Service to one task and run. Relay callers cannot
// supply identity or access another task's inbox.
type CrewChannel interface {
	Post(context.Context, CrewPost) (CrewReceipt, error)
	Inbox(context.Context, uint64) (CrewInbox, error)
}
type CrewClaims struct {
	Tests string `json:"tests"`
}
type CrewPost struct {
	ClientID string      `json:"client_id"`
	Kind     string      `json:"kind"`
	Text     string      `json:"text"`
	Claims   *CrewClaims `json:"claims,omitempty"`
}
type CrewMessage struct {
	ID        string      `json:"id"`
	Direction string      `json:"direction"`
	Kind      string      `json:"kind"`
	Text      string      `json:"text"`
	Claims    *CrewClaims `json:"claims,omitempty"`
	ClientID  string      `json:"client_id,omitempty"`
}
type CrewRead struct {
	MessageIDs []string `json:"message_ids"`
}
type CrewReceipt struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}
type MessageReceipt struct {
	Namespace string `json:"namespace"`
	TaskID    string `json:"task_id"`
	CrewReceipt
}
type InboxMessage struct {
	ID       string    `json:"id"`
	Sequence uint64    `json:"sequence"`
	Text     string    `json:"text"`
	Time     time.Time `json:"time"`
}
type CrewInbox struct {
	Messages []InboxMessage `json:"messages"`
	Next     uint64         `json:"next"`
}

var crewClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var crewKeysBucket = []byte("crew-message-keys")

func validCrewText(text string) bool {
	return len(text) > 0 && len(text) <= 8192 && utf8.ValidString(text)
}
func validateCrewPost(p CrewPost) error {
	if !crewClientID.MatchString(p.ClientID) || !validCrewText(p.Text) {
		return ErrInvalid
	}
	switch p.Kind {
	case "report", "ask", "blocked":
		if p.Claims != nil {
			return ErrInvalid
		}
	case "submit":
		if p.Claims == nil || (p.Claims.Tests != "pass" && p.Claims.Tests != "fail" && p.Claims.Tests != "not_run") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func taskEvents(tx *bolt.Tx, id string) ([]Event, error) {
	events := []Event{}
	b := tx.Bucket(eventsBucket).Bucket([]byte(id))
	if b == nil {
		return events, nil
	}
	err := b.ForEach(func(_, raw []byte) error {
		var event Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	})
	return events, err
}
func appendCrewMessage(tx *bolt.Tx, task *Task, message CrewMessage) (CrewReceipt, error) {
	if err := appendEvent(tx, task, Event{Kind: "crew.message", Message: &message}); err != nil {
		return CrewReceipt{}, err
	}
	return CrewReceipt{ID: message.ID, Sequence: tx.Bucket(eventsBucket).Bucket([]byte(task.ID)).Sequence()}, nil
}

type runCrew struct {
	service                  *Service
	namespace, taskID, runID string
}

func (c runCrew) active(tx *bolt.Tx) (*Task, error) {
	task, err := readTask(tx, c.namespace, c.taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != Running || len(task.Runs) == 0 || task.latest().ID != c.runID {
		return nil, ErrConflict
	}
	return task, nil
}
func (c runCrew) Post(ctx context.Context, p CrewPost) (receipt CrewReceipt, err error) {
	if err = validateCrewPost(p); err != nil {
		return
	}
	s := c.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.available(); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		task, e := c.active(tx)
		if e != nil {
			return e
		}
		events, e := taskEvents(tx, task.ID)
		if e != nil {
			return e
		}
		count := 0
		for _, event := range events {
			m := event.Message
			if event.RunID != c.runID || m == nil || m.Direction != "from_worker" {
				continue
			}
			count++
			if m.ClientID == p.ClientID {
				previous, _ := json.Marshal(CrewPost{ClientID: m.ClientID, Kind: m.Kind, Text: m.Text, Claims: m.Claims})
				requested, _ := json.Marshal(p)
				if !bytes.Equal(previous, requested) {
					return ErrConflict
				}
				receipt = CrewReceipt{ID: m.ID, Sequence: event.Sequence}
				return nil
			}
		}
		if count >= 200 {
			return ErrFull
		}
		receipt, e = appendCrewMessage(tx, task, CrewMessage{ID: uuid.NewString(), Direction: "from_worker", Kind: p.Kind, Text: p.Text, Claims: p.Claims, ClientID: p.ClientID})
		return e
	})
	return
}
func (c runCrew) Inbox(ctx context.Context, after uint64) (inbox CrewInbox, err error) {
	inbox = CrewInbox{Messages: []InboxMessage{}, Next: after}
	s := c.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.available(); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		task, e := c.active(tx)
		if e != nil {
			return e
		}
		events, e := taskEvents(tx, task.ID)
		if e != nil {
			return e
		}
		ids := []string{}
		for _, event := range events {
			m := event.Message
			if event.Sequence <= after || m == nil || m.Direction != "to_worker" {
				continue
			}
			inbox.Messages = append(inbox.Messages, InboxMessage{ID: m.ID, Sequence: event.Sequence, Text: m.Text, Time: event.Time})
			inbox.Next = event.Sequence
			ids = append(ids, m.ID)
			if len(ids) == 50 {
				break
			}
		}
		if len(ids) > 0 {
			return appendEvent(tx, task, Event{Kind: "crew.read", Read: &CrewRead{MessageIDs: ids}})
		}
		return nil
	})
	return
}

// Message durably queues a foreman's note even when the worker has stopped.
func (s *Service) Message(namespace, id, text, key string) (out MessageReceipt, err error) {
	if namespace == "" || id == "" || !validCrewText(text) || len(key) < 1 || len(key) > 128 || !utf8.ValidString(key) {
		return out, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.available(); err != nil {
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		task, e := readTask(tx, namespace, id)
		if e != nil {
			return e
		}
		keys, e := tx.CreateBucketIfNotExists(crewKeysBucket)
		if e != nil {
			return e
		}
		keys, e = keys.CreateBucketIfNotExists([]byte(task.ID))
		if e != nil {
			return e
		}
		var record struct {
			Text    string
			Receipt CrewReceipt
		}
		if raw := keys.Get([]byte(key)); raw != nil {
			if e = json.Unmarshal(raw, &record); e != nil {
				return e
			}
			if record.Text != text {
				return ErrConflict
			}
		} else {
			record.Text = text
			record.Receipt, e = appendCrewMessage(tx, task, CrewMessage{ID: uuid.NewString(), Direction: "to_worker", Kind: "note", Text: text})
			if e != nil {
				return e
			}
			raw, e := json.Marshal(record)
			if e != nil {
				return e
			}
			if e = keys.Put([]byte(key), raw); e != nil {
				return e
			}
		}
		out = MessageReceipt{Namespace: namespace, TaskID: id, CrewReceipt: record.Receipt}
		return nil
	})
	return
}
func unreadCrewMessages(tx *bolt.Tx, task *Task) ([]InboxMessage, error) {
	events, err := taskEvents(tx, task.ID)
	if err != nil {
		return nil, err
	}
	read := map[string]bool{}
	for _, event := range events {
		if event.Read != nil {
			for _, id := range event.Read.MessageIDs {
				read[id] = true
			}
		}
	}
	unread := []InboxMessage{}
	for _, event := range events {
		m := event.Message
		if m != nil && m.Direction == "to_worker" && !read[m.ID] {
			unread = append(unread, InboxMessage{ID: m.ID, Sequence: event.Sequence, Text: m.Text, Time: event.Time})
		}
	}
	return unread, nil
}
func resumeCrewInput(tx *bolt.Tx, task *Task) error {
	unread, err := unreadCrewMessages(tx, task)
	if err != nil {
		return err
	}
	if len(unread) == 0 {
		return nil
	}
	run := task.latest()
	run.Input += "\n\nUnread messages from the foreman:"
	ids := []string{}
	for _, m := range unread {
		run.Input += fmt.Sprintf("\n- [%s] %s", m.ID, m.Text)
		ids = append(ids, m.ID)
	}
	return appendEvent(tx, task, Event{Kind: "crew.read", Read: &CrewRead{MessageIDs: ids}})
}

// crewOutcome folds one run's worker messages into its outcome and, for a
// final submit, the claimed test result. The last submit/blocked/ask wins.
func crewOutcome(events []Event, runID string) (outcome, tests string) {
	outcome = "none"
	for _, event := range events {
		m := event.Message
		if event.RunID != runID || m == nil || m.Direction != "from_worker" {
			continue
		}
		switch m.Kind {
		case "submit":
			outcome, tests = "submitted", ""
			if m.Claims != nil {
				tests = m.Claims.Tests
			}
		case "blocked":
			outcome, tests = "blocked", ""
		case "ask":
			outcome, tests = "asked", ""
		}
	}
	return outcome, tests
}

// finishCrewOutcome recomputes both run.Outcome and, when the run carries an
// Acceptance record, run.Acceptance.FalseGreen from one scan of the crew
// messages. finish (called just before this by finishRun) may rewrite
// Acceptance.State (e.g. to "interrupted") without updating FalseGreen;
// recomputing it here keeps the two consistent instead of leaving a stale
// "submitted tests=pass" false green attached to a state that no longer
// satisfies the truth table.
func finishCrewOutcome(tx *bolt.Tx, task *Task) error {
	events, err := taskEvents(tx, task.ID)
	if err != nil {
		return err
	}
	run := task.latest()
	outcome, tests := crewOutcome(events, run.ID)
	run.Outcome = outcome
	if run.Acceptance != nil {
		run.Acceptance.FalseGreen = falseGreen(outcome, tests, run.Acceptance.State)
	}
	return nil
}
func crewHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalid):
		return 400
	case errors.Is(err, ErrConflict):
		return 409
	case errors.Is(err, ErrFull):
		return 429
	default:
		return 503
	}
}
