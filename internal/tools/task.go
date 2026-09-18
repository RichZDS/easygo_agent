package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	agentruntime "easygo-agent/internal/agent/runtime"
	"easygo-agent/internal/task"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type taskIDInput struct {
	TaskID string `json:"task_id" jsonschema:"required"`
}

func taskOwner(ctx context.Context, mutate bool) (task.Owner, error) {
	identity, ok := agentruntime.InvocationIdentityFromContext(ctx)
	if !ok || identity.Username == "" {
		return task.Owner{}, fmt.Errorf("trusted task owner is unavailable")
	}
	if mutate && identity.Internal {
		return task.Owner{}, fmt.Errorf("internal notifications may only query tasks")
	}
	return task.Owner{User: identity.Username, Session: identity.SessionID, Run: identity.RunID}, nil
}

// taskView keeps native execution history in storage instead of copying it into
// the parent context. Unresolved calls remain visible for explicit recovery.
func taskView(t task.Task) map[string]any {
	return map[string]any{"task_id": t.ID, "version": t.Version, "status": t.Status, "brief": t.Brief, "plan": t.Plan, "progress": t.Progress, "result": t.Result, "reason": t.Reason, "pending_calls": t.Checkpoint.Calls}
}
func NewTaskTools(service *task.Service) ([]tool.BaseTool, error) {
	var items []tool.BaseTool
	roles := []string{}
	if service.Engine != nil {
		for name, role := range service.Engine.Config.Roles {
			roles = append(roles, name+": "+role.Instruction)
		}
	}
	sort.Strings(roles)
	spawn, err := utils.InferTool("spawn_subagent", "Submit independent background work with a configured role, goal, constraints, acceptance criteria and materials. Returns an accepted task, not completion. Reuse idempotency_key for retries. Available roles: "+strings.Join(roles, "; "), func(ctx context.Context, in task.Brief) (map[string]any, error) {
		o, err := taskOwner(ctx, true)
		if err != nil {
			return nil, err
		}
		t, err := service.Spawn(ctx, o, in)
		if err != nil {
			return nil, err
		}
		return taskView(t), nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, spawn)
	get, err := utils.InferTool("get_task", "Read a task in the current user's session, including evidence and unresolved calls.", func(ctx context.Context, in taskIDInput) (map[string]any, error) {
		o, err := taskOwner(ctx, false)
		if err != nil {
			return nil, err
		}
		t, err := service.Store.Get(ctx, o, in.TaskID)
		if err != nil {
			return nil, err
		}
		return taskView(t), nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, get)
	list, err := utils.InferTool("list_tasks", "List background tasks in the current session, including tasks from earlier turns.", func(ctx context.Context, in struct{}) ([]map[string]any, error) {
		o, err := taskOwner(ctx, false)
		if err != nil {
			return nil, err
		}
		tasks, err := service.Store.List(ctx, o)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, t := range tasks {
			out = append(out, taskView(t))
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, list)
	cancel, err := utils.InferTool("cancel_task", "Cancel a background task owned by this session.", func(ctx context.Context, in taskIDInput) (map[string]any, error) {
		o, err := taskOwner(ctx, true)
		if err != nil {
			return nil, err
		}
		t, err := service.Store.Cancel(ctx, o, in.TaskID)
		if err != nil {
			return nil, err
		}
		return taskView(t), nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, cancel)
	resume, err := utils.InferTool("resume_task", "Continue a terminal task under the same ID with a fresh execution version. An uncertain call requires an explicit user decision: retry, or result with a verified result string. Running tasks cannot be resumed.", func(ctx context.Context, in struct {
		TaskID       string                   `json:"task_id" jsonschema:"required"`
		Instructions string                   `json:"instructions"`
		Decisions    map[string]task.Decision `json:"decisions"`
	}) (map[string]any, error) {
		o, err := taskOwner(ctx, true)
		if err != nil {
			return nil, err
		}
		t, err := service.Store.Resume(ctx, o, in.TaskID, task.Resume{ParentRun: o.Run, Instructions: in.Instructions, Decisions: in.Decisions})
		if err != nil {
			return nil, err
		}
		return taskView(t), nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, resume)
	plan, err := utils.InferTool("update_plan", "Update the plan for a queued or terminal task. Running subagents own their plan through report_progress.", func(ctx context.Context, in struct {
		TaskID string          `json:"task_id" jsonschema:"required"`
		Plan   []task.PlanStep `json:"plan" jsonschema:"required"`
	}) (map[string]any, error) {
		o, err := taskOwner(ctx, true)
		if err != nil {
			return nil, err
		}
		t, err := service.Store.UpdatePlan(ctx, o, in.TaskID, in.Plan)
		if err != nil {
			return nil, err
		}
		return taskView(t), nil
	})
	if err != nil {
		return nil, err
	}
	items = append(items, plan)
	return items, nil
}
