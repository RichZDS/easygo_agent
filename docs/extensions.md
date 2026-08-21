# Deferred Extensions

This template intentionally leaves the following capabilities unimplemented. Add them only in a derived project with concrete requirements.

## Memory

Do not add an empty Memory interface to this template. First decide which semantics the derived project needs:

- current conversation history;
- durable messages;
- Eino checkpoints and resume;
- summarization;
- retrieval or long-term knowledge.

Define the seam only after at least one real implementation and its test adapter are known. The current TUI's in-process message slice is interaction state, not a Memory subsystem.

## MCP

Do not invent an MCP interface before choosing:

- client, server, or both;
- stdio, HTTP, or another transport;
- Tool discovery and refresh behavior;
- trust, consent, and credential rules.

Once chosen, adapt MCP Tools to Eino's native Tool interface rather than creating a parallel Tool abstraction.

## HTTP or RPC

Add a transport adapter over `gateway.Runner`. The derived project owns:

- request and response serialization;
- streaming protocol such as SSE or WebSocket;
- authentication and authorization;
- rate limits, quotas, timeouts, and request size limits;
- deployment, health checks, and graceful server shutdown.

Do not place transport metadata or authorization policy inside Gateway requests.

## Multiple Models and AI Gateway Policy

The template binds one model at startup. Add routing only after the derived project has explicit rules for model selection, fallback, retry, cost, latency, residency, or capability. Keep those policies outside the TUI and do not reintroduce provider-database or enabled-state checks without a real product requirement.

## OTLP Export

Replace the stdout exporter in `internal/app`/`internal/observability`. Gateway and TUI interfaces should not change. The derived project owns Collector endpoints, sampling, resource attributes, credentials, and data-retention policy.
