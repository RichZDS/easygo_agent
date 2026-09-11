FROM node:22-bookworm-slim AS node

FROM golang:1.25-bookworm

LABEL io.easygo.sandbox.runtime="true"

RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        bash \
        build-essential \
        ca-certificates \
        coreutils \
        findutils \
        git \
        jq \
        make \
        procps \
        python3 \
        python3-pip \
        python3-venv \
        ripgrep \
        tar \
        tini \
        unzip \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 1000 sandbox \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash sandbox \
    && groupadd --gid 1001 sandbox-supervisor \
    && useradd --uid 1001 --gid 1001 --no-create-home --shell /usr/sbin/nologin sandbox-supervisor \
    && mkdir -p \
        /workspace/.cache/go-build \
        /workspace/.cache/go-mod \
        /workspace/.cache/npm \
        /workspace/.cache/python \
        /workspace/.tmp/go \
        /workspace/.home \
    && chown -R 1000:1000 /workspace

COPY --from=node /usr/local/ /usr/local/
COPY deploy/sandbox/runtime-entrypoint.sh /usr/local/bin/easygo-sandbox-init

RUN chmod 0555 /usr/local/bin/easygo-sandbox-init

ENV HOME=/workspace/.home \
    XDG_CACHE_HOME=/workspace/.cache \
    GOCACHE=/workspace/.cache/go-build \
    GOMODCACHE=/workspace/.cache/go-mod \
    GOTMPDIR=/workspace/.tmp/go \
    GOPROXY=off \
    GOSUMDB=off \
    GOTOOLCHAIN=local \
    PIP_NO_INDEX=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONPYCACHEPREFIX=/workspace/.cache/python \
    TMPDIR=/workspace/.tmp \
    npm_config_cache=/workspace/.cache/npm \
    npm_config_audit=false \
    npm_config_fund=false \
    npm_config_offline=true \
    npm_config_update_notifier=false

WORKDIR /workspace

# Docker copies this pre-owned /workspace skeleton into a newly attached empty
# named volume. The idle supervisor uses a separate unprivileged identity so
# the controller can reliably remove every leftover UID 1000 process after a
# command without killing PID 1. Model-controlled execs always use UID 1000.
USER 1001:1001
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/easygo-sandbox-init"]
