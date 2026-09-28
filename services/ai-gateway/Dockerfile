# Build context is the repository root.
# packages/rpc-go is copied beside this module so
# replace ../../packages/rpc-go resolves inside the image.

FROM golang:1.25-bookworm AS build

COPY packages/rpc-go /src/packages/rpc-go
COPY services/ai-gateway /src/services/ai-gateway
WORKDIR /src/services/ai-gateway
RUN go build -trimpath -o /out/server ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/server /usr/local/bin/server
USER 1000:1000
EXPOSE 8441
ENTRYPOINT ["/usr/local/bin/server"]
CMD ["--config", "/run/easygo/config.json"]
