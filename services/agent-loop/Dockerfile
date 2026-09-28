# Build context is the repository root.
# node:22-bookworm-slim tracks the Node 22 line. This service requires
# Node >= 22.23 (package.json engines and the built-in node:sqlite).
# The check fails the image build when the pulled tag is older.
# package-lock.json must be present; npm ci will not invent a lockfile.

FROM node:22-bookworm-slim AS build

RUN node -e 'const p=process.versions.node.split(".").map(Number); if (p[0] < 22 || (p[0] === 22 && p[1] < 23)) { console.error("Node.js >= 22.23.0 required, found " + process.version); process.exit(1); }'
WORKDIR /app
COPY services/agent-loop/package*.json ./
RUN npm ci
COPY services/agent-loop ./
RUN npm run build

FROM node:22-bookworm-slim

RUN node -e 'const p=process.versions.node.split(".").map(Number); if (p[0] < 22 || (p[0] === 22 && p[1] < 23)) { console.error("Node.js >= 22.23.0 required, found " + process.version); process.exit(1); }'
WORKDIR /app
COPY services/agent-loop/package*.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY --from=build /app/dist ./dist
RUN mkdir -p /data && chown 1000:1000 /data
USER 1000:1000
EXPOSE 8442
CMD ["node", "dist/server.js", "--config", "/run/easygo/config.json"]
