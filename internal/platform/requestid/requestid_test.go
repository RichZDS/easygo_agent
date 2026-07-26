package requestid_test

import (
	"context"
	"testing"

	"easygo-agent/internal/platform/requestid"
)

func TestWithFrom(t *testing.T) {
	ctx := requestid.With(context.Background(), "abc123")
	if got := requestid.From(ctx); got != "abc123" {
		t.Fatalf("From = %q, want abc123", got)
	}
	if got := requestid.From(context.Background()); got != "" {
		t.Fatalf("empty context From = %q, want empty", got)
	}
}

func TestWithDeadlinePreserves(t *testing.T) {
	ctx := requestid.With(context.Background(), "rid")
	deadlineCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := requestid.From(deadlineCtx); got != "rid" {
		t.Fatalf("From after WithCancel = %q, want rid", got)
	}
}

func TestBindCurrentUnbind(t *testing.T) {
	if got := requestid.Current(); got != "" {
		t.Fatalf("Current before Bind = %q, want empty", got)
	}
	requestid.Bind("t1")
	if got := requestid.Current(); got != "t1" {
		t.Fatalf("Current = %q, want t1", got)
	}
	requestid.Bind("t2")
	if got := requestid.Current(); got != "t2" {
		t.Fatalf("nested Current = %q, want t2", got)
	}
	requestid.Unbind()
	if got := requestid.Current(); got != "t1" {
		t.Fatalf("after Unbind = %q, want t1", got)
	}
	requestid.Unbind()
	if got := requestid.Current(); got != "" {
		t.Fatalf("after all Unbind = %q, want empty", got)
	}
}

func TestAttach(t *testing.T) {
	ctx := requestid.With(context.Background(), "att")
	done := requestid.Attach(ctx)
	if got := requestid.Current(); got != "att" {
		t.Fatalf("Current after Attach = %q, want att", got)
	}
	done()
	if got := requestid.Current(); got != "" {
		t.Fatalf("Current after done = %q, want empty", got)
	}
}
