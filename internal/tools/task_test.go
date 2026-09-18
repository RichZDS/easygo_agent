package tools

import (
	"context"
	"strings"
	"testing"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/task"
	"github.com/cloudwego/eino/components/tool"
)

func TestTaskToolsRejectUntrustedAndNotificationMutation(t *testing.T) {
	service := &task.Service{Store: task.NewMemory()}
	items, err := NewTaskTools(service)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentruntime.WithInvocationIdentity(context.Background(), agentruntime.InvocationIdentity{Username: "alice", SessionID: "session", RunID: "run", Internal: true})
	for _, item := range items {
		info, _ := item.Info(ctx)
		inv := item.(tool.InvokableTool)
		if _, err = inv.InvokableRun(context.Background(), `{}`); err == nil {
			t.Fatalf("%s accepted no identity", info.Name)
		}
		raw, err := inv.InvokableRun(ctx, `{}`)
		if info.Name == "list_tasks" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if info.Name == "get_task" {
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "internal notifications") {
			t.Fatalf("%s: %q %v", info.Name, raw, err)
		}
	}
}
