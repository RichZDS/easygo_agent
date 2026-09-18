// Package toolregistry owns capability schemas, dispatch and recovery policy.
package toolregistry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type Retry string

const (
	Unsafe     Retry = "uncertain_write"
	ReadOnly   Retry = "read_only"
	Idempotent Retry = "idempotent"
)

type Entry struct {
	Info   *schema.ToolInfo
	Tool   tool.InvokableTool
	Group  string
	Retry  Retry
	Hidden bool
}
type Registry struct{ entries map[string]Entry }

func New() *Registry { return &Registry{entries: map[string]Entry{}} }
func (r *Registry) Register(ctx context.Context, t tool.BaseTool, group string, retry Retry, hidden bool) error {
	info, err := t.Info(ctx)
	if err != nil {
		return err
	}
	inv, ok := t.(tool.InvokableTool)
	if !ok {
		return fmt.Errorf("tool %s is not invokable", info.Name)
	}
	if _, ok := r.entries[info.Name]; ok {
		return fmt.Errorf("duplicate tool %s", info.Name)
	}
	if retry == "" {
		retry = Unsafe
	}
	r.entries[info.Name] = Entry{info, inv, group, retry, hidden}
	return nil
}
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) Lookup(name string) (Entry, bool) { e, ok := r.entries[name]; return e, ok }
func (r *Registry) Invoke(ctx context.Context, name, args string) (string, error) {
	e, ok := r.Lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown or disallowed tool %q", name)
	}
	return e.Tool.InvokableRun(ctx, args)
}
func (r *Registry) Native(ctx context.Context) ([]tool.BaseTool, error) {
	result := []tool.BaseTool{}
	hidden := false
	for _, name := range r.Names() {
		e := r.entries[name]
		if e.Hidden {
			hidden = true
		} else {
			result = append(result, e.Tool)
		}
	}
	if hidden {
		dispatcher, err := r.Dispatcher()
		if err != nil {
			return nil, err
		}
		result = append(result, dispatcher)
	}
	return result, nil
}
func (r *Registry) Dispatcher() (tool.InvokableTool, error) {
	return utils.InferTool("call_tool", "Invoke a registered capability by name using its skill documentation. Arguments is a JSON object.", func(ctx context.Context, in struct {
		Name      string          `json:"name" jsonschema:"required"`
		Arguments json.RawMessage `json:"arguments"`
	}) (map[string]any, error) {
		if in.Name == "call_tool" {
			return nil, fmt.Errorf("recursive dispatch is not allowed")
		}
		args := string(in.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		raw, err := r.Invoke(ctx, in.Name, args)
		if err != nil {
			return nil, err
		}
		var result any
		if json.Unmarshal([]byte(raw), &result) != nil {
			result = raw
		}
		return map[string]any{"name": in.Name, "result": result}, nil
	})
}

// Subset excludes routing/control/sandbox capabilities even if configured by mistake.
func (r *Registry) Subset(ctx context.Context, names []string, readonly bool) (*Registry, error) {
	out := New()
	for _, name := range names {
		e, ok := r.Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown role tool %q", name)
		}
		if e.Group == "sandbox" || e.Group == "tasks" || name == "call_tool" {
			return nil, fmt.Errorf("tool %q is unavailable to subagents", name)
		}
		if readonly && e.Retry != ReadOnly {
			return nil, fmt.Errorf("analyst tool %q must be read-only", name)
		}
		if err := out.Register(ctx, e.Tool, e.Group, e.Retry, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (r *Registry) Version(ctx context.Context) (string, error) {
	var b strings.Builder
	for _, name := range r.Names() {
		e := r.entries[name]
		params, err := e.Info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(struct {
			Name        string
			Description string
			Schema      any
		}{e.Info.Name, e.Info.Desc, params})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s:%s:%s:%s\n", name, e.Group, e.Retry, data)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String()))), nil
}

type key struct{}

func WithIdempotencyKey(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, key{}, value)
}
func IdempotencyKey(ctx context.Context) string { value, _ := ctx.Value(key{}).(string); return value }
