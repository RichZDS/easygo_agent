# Trusted controller only. Native CLIs live in deploy/runtime/Dockerfile.
# Build context is the repository root. Only this service receives Docker access.
FROM golang:1.25-bookworm AS build
COPY packages/rpc-go /src/packages/rpc-go
COPY services/workshop /src/services/workshop
WORKDIR /src/services/workshop
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server
FROM docker:27.5.1-cli AS docker-cli
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* && mkdir /data && chown 1000:1000 /data
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/server /usr/local/bin/server
WORKDIR /
USER 1000:1000
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/server"]
CMD ["--config", "/run/easygo/config.json"]
