package usermemory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"easygo-agent/internal/testutil"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type extractionModel struct {
	t      *testing.T
	calls  int
	result *schema.AgenticMessage
	err    error
}

func (m *extractionModel) Generate(_ context.Context, in []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	m.calls++
	o := model.GetCommonOptions(nil, opts...)
	if len(o.Tools) != 0 || o.ToolChoice == nil || *o.ToolChoice != schema.ToolChoiceForbidden {
		m.t.Error("memory extraction was not explicitly tool-free")
	}
	if len(in) != 2 || in[0].Role != schema.AgenticRoleTypeSystem || in[1].Role != schema.AgenticRoleTypeUser {
		m.t.Error("memory instruction and untrusted payload are not separated")
	}
	return m.result, m.err
}
func (m *extractionModel) Stream(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	return nil, errors.New("memory extraction should use a single completion")
}

func TestMemoryAgentUsesOneToolFreeCompletion(t *testing.T) {
	m := &extractionModel{t: t, result: testutil.Text("[]")}
	a, err := NewModelAgent(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := a.Extract(context.Background(), "alice", nil); err != nil || len(candidates) != 0 {
		t.Fatalf("extract: %v", err)
	}
	if drafts, err := a.Reconcile(context.Background(), "alice", nil, nil); err != nil || len(drafts) != 0 {
		t.Fatalf("reconcile: %v", err)
	}
	if m.calls != 2 {
		t.Fatalf("completion calls=%d", m.calls)
	}
}

func TestMemoryAgentRejectsInvalidModelCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *schema.AgenticMessage
		err    error
	}{
		{"missing", nil, nil},
		{"empty", testutil.Text(" "), nil},
		{"tool", testutil.ToolCall("unexpected", "{}"), nil},
		{"invalid_json", testutil.Text("[] trailing"), nil},
		{"error", nil, errors.New("upstream failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &extractionModel{t: t, result: tc.result, err: tc.err}
			a, err := NewModelAgent(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.Extract(context.Background(), "alice", nil); err == nil {
				t.Fatal("invalid completion accepted")
			}
			if m.calls != 1 {
				t.Fatal("unexpected memory retry")
			}
		})
	}
	m := &extractionModel{t: t, result: testutil.Text("[]")}
	a, _ := NewModelAgent(context.Background(), m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Extract(ctx, "alice", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if _, err := NewModelAgent(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("nil model error=%v", err)
	}
}
