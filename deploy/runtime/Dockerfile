# Build context: repository root. Separate from the trusted workshop controller.
FROM golang:1.25-bookworm AS build
COPY packages/rpc-go /src/packages/rpc-go
COPY services/workshop /src/services/workshop
WORKDIR /src/services/workshop
RUN CGO_ENABLED=0 go build -trimpath -o /out/task-shim ./cmd/task-shim

FROM node:26-bookworm-slim
ARG CODEX_VERSION=0.157.1
ARG CLAUDE_CODE_VERSION=2.1.281
ARG PI_VERSION=0.87.1
ARG OPENCLAW_VERSION=2026.9.6
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates git python3 \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit \
 "@openai/codex@${CODEX_VERSION}" "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 "@earendil-works/pi-coding-agent@${PI_VERSION}" "openclaw@${OPENCLAW_VERSION}" \
 && npm cache clean --force
COPY --from=build /out/task-shim /usr/local/bin/task-shim
RUN mkdir -p /workspace /run/easygo-relay && chown 1000:1000 /workspace
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/task-shim"]
