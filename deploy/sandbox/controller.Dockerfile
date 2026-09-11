FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/sandbox-controller ./cmd/sandbox-controller
COPY internal/sandbox ./internal/sandbox
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sandbox-controller ./cmd/sandbox-controller

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/sandbox-controller /usr/local/bin/sandbox-controller

USER 0:0
ENTRYPOINT ["/usr/local/bin/sandbox-controller"]
