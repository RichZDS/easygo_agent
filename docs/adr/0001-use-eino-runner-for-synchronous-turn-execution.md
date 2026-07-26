---
status: accepted
---

# Use Eino Runner for synchronous Turn execution

EasyGo will make the Eino ADK Runner the execution boundary for a Turn and keep that execution inside one streaming HTTP request. This replaces the custom Outbox, Redis worker, replayable event stream, provider HTTP fallback, and remote cancellation path so that the first version follows Eino's native Agent, Runner, AgentEvent, adapter, middleware, callback, retry, and cancellation lifecycles as closely as possible; MySQL remains authoritative for Sessions, Turns, Messages, model revisions, and audit data.

## Consequences

A client disconnect cancels the Turn, only one Turn may be active per Session, and completed execution cannot be reattached or replayed. Partial assistant output from a cancelled or failed Turn remains visible but is excluded from later model context.

Model adapters must be explicitly registered in the Eino adapter Registry. Every Turn creates its own Model, summarization middleware, ChatModelAgent, and Runner while sharing only the HTTP transport. Agent instructions are versioned on the Turn, and durable Message fields mirror the current Eino `schema.Message` rather than a custom provider payload.

If the product later requires long-running background work, human interruption and resumption, or reconnectable execution, the design will be revisited around Eino CheckPointStore rather than restoring the previous custom execution framework.
