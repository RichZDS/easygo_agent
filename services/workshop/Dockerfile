# Build context is the repository root.
# The server is a Go binary. The runtime image is Node so the pinned
# Codex and Claude Code CLIs can be installed beside it.
# Host npm is not used, and this image does not receive a Docker socket.

FROM golang:1.25-bookworm AS build

COPY packages/rpc-go /src/packages/rpc-go
COPY services/workshop /src/services/workshop
WORKDIR /src/services/workshop
RUN go build -trimpath -o /out/server ./cmd/server

FROM node:22-bookworm-slim

ARG CODEX_VERSION=0.156.0
ARG CLAUDE_CODE_VERSION=2.1.281

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        python3 \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g --no-fund --no-audit \
        "@openai/codex@${CODEX_VERSION}" \
        "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    && npm cache clean --force

COPY --from=build /out/server /usr/local/bin/server
WORKDIR /
RUN mkdir -p /data && chown 1000:1000 /data
USER 1000:1000
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/server"]
CMD ["--config", "/run/easygo/config.json"]
