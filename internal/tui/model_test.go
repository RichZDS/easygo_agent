package tui

import (
	"context"
	agentruntime "easygo-agent/internal/agent/runtime"
	"github.com/charmbracelet/bubbletea"
	"testing"
	"time"
)

type fakeConversation struct{ run *fakeRun }

func (f fakeConversation) Start(string) (agentruntime.Run, error) { return f.run, nil }

type fakeRun struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (f *fakeRun) Next() agentruntime.Event {
	<-f.ctx.Done()
	return agentruntime.Event{Kind: agentruntime.EventCanceled}
}
func (f *fakeRun) Cancel() { f.cancel() }

func TestUIRemainsCancelableDuringRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(fakeConversation{&fakeRun{ctx, cancel}})
	m.input.SetValue("hi")
	cmd := m.submit()
	if cmd == nil || m.state != stateRunning {
		t.Fatal("run did not start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case message := <-done:
		m.Update(message)
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	if m.state != stateIdle || m.activeRun != nil {
		t.Fatal("cancel did not restore idle state")
	}
}
