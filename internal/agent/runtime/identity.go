package agentruntime

import (
	"context"
	"strings"
)

// InvocationIdentity identifies the durable conversation and run that own a
// tool invocation. Values in this context are trusted runtime metadata and are
// never accepted from model-provided tool arguments.
type InvocationIdentity struct {
	Username  string
	Internal  bool
	SessionID string
	RunID     string
}

type invocationIdentityKey struct{}

// WithInvocationIdentity attaches trusted session and run ownership metadata
// to the context propagated through Eino and into tools.
func WithInvocationIdentity(ctx context.Context, identity InvocationIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity.SessionID = strings.TrimSpace(identity.SessionID)
	identity.RunID = strings.TrimSpace(identity.RunID)
	return context.WithValue(ctx, invocationIdentityKey{}, identity)
}

// InvocationIdentityFromContext returns the trusted owner of a tool call.
// Both IDs must be present; callers must fail closed when ok is false.
func InvocationIdentityFromContext(ctx context.Context) (identity InvocationIdentity, ok bool) {
	if ctx == nil {
		return InvocationIdentity{}, false
	}
	identity, ok = ctx.Value(invocationIdentityKey{}).(InvocationIdentity)
	if !ok || strings.TrimSpace(identity.SessionID) == "" || strings.TrimSpace(identity.RunID) == "" {
		return InvocationIdentity{}, false
	}
	return identity, true
}
