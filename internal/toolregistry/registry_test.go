package toolregistry

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool/utils"
)

func TestRegistryDispatchAndSchemaVersion(t *testing.T) {
	ctx := context.Background()
	r := New()
	one, err := utils.InferTool("lookup", "lookup", func(ctx context.Context, in struct {
		ID string `json:"id"`
	}) (string, error) { return in.ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Register(ctx, one, "lookup", ReadOnly, true); err != nil {
		t.Fatal(err)
	}
	items, err := r.Native(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("native %d %v", len(items), err)
	}
	dispatch, _ := r.Dispatcher()
	got, err := dispatch.InvokableRun(ctx, `{"name":"lookup","arguments":{"id":"evidence"}}`)
	if err != nil || !strings.Contains(got, "evidence") {
		t.Fatalf("dispatch %q %v", got, err)
	}
	if _, err = dispatch.InvokableRun(ctx, `{"name":"missing"}`); err == nil {
		t.Fatal("unknown dispatched")
	}
	if _, err = dispatch.InvokableRun(ctx, `{"name":"call_tool"}`); err == nil {
		t.Fatal("recursive dispatched")
	}
	version, err := r.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r2 := New()
	two, _ := utils.InferTool("lookup", "lookup", func(ctx context.Context, in struct {
		Number int `json:"number"`
	}) (int, error) { return in.Number, nil })
	_ = r2.Register(ctx, two, "lookup", ReadOnly, true)
	changed, err := r2.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed == version {
		t.Fatal("schema change invisible to recovery")
	}
	if err = r.Register(ctx, one, "lookup", ReadOnly, true); err == nil {
		t.Fatal("duplicate registered")
	}
}
func TestUnknownRetryDefaultsToUnsafe(t *testing.T) {
	ctx := context.Background()
	r := New()
	item, _ := utils.InferTool("write", "write", func(context.Context, struct{}) (string, error) { return "ok", nil })
	if err := r.Register(ctx, item, "business", "", false); err != nil {
		t.Fatal(err)
	}
	entry, _ := r.Lookup("write")
	if entry.Retry != Unsafe {
		t.Fatal("unknown policy retried automatically")
	}
	if _, err := r.Subset(ctx, []string{"write"}, true); err == nil {
		t.Fatal("analyst write allowed")
	}
}
