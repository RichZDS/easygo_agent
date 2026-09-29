# One-shot init for compose.yaml. Build context is the repository root.
# Runs as root so it can hand fresh bind-mount directories to UID 1000.
FROM node:22-bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssl \
 && rm -rf /var/lib/apt/lists/*
COPY scripts/configure-platform.mjs scripts/dev-pki.sh /opt/easygo/scripts/
COPY services/agent-loop/src/platform/client-address.mjs /opt/easygo/services/agent-loop/src/platform/
COPY services/ai-gateway/config.deepseek.example.json /opt/easygo/services/ai-gateway/
COPY services/workshop/config.runtimes.example.json /opt/easygo/services/workshop/
COPY deploy/init/init.mjs /opt/easygo/deploy/init/
ENTRYPOINT ["node", "/opt/easygo/deploy/init/init.mjs"]
