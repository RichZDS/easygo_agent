#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
readonly COMPOSE_FILE="$REPO_DIR/compose.sandbox-test.yaml"
readonly TEST_NAMESPACE="easygo-agent-it"
readonly TEST_EXPIRY_NAMESPACE="easygo-agent-it-expiry"

DOCKER_BIN="${DOCKER_BIN:-docker}"
SANDBOX_TEST_PORT="${SANDBOX_TEST_PORT:-18787}"
export DOCKER_BIN SANDBOX_TEST_PORT

if ! command -v "$DOCKER_BIN" >/dev/null 2>&1; then
  echo "Docker CLI not found: $DOCKER_BIN" >&2
  exit 2
fi
if ! "$DOCKER_BIN" info >/dev/null 2>&1; then
  echo "Docker daemon is not running. Start it explicitly, then rerun this script." >&2
  exit 2
fi

if [[ -z "${DOCKER_SOCKET_PATH:-}" ]]; then
  docker_endpoint="$("$DOCKER_BIN" context inspect --format '{{ (index .Endpoints "docker").Host }}')"
  if [[ "$docker_endpoint" != unix://* ]]; then
    echo "The integration Controller requires a local unix Docker socket; got $docker_endpoint" >&2
    exit 2
  fi
  DOCKER_SOCKET_PATH="${docker_endpoint#unix://}"
  export DOCKER_SOCKET_PATH
fi
if [[ ! -S "$DOCKER_SOCKET_PATH" ]]; then
  echo "Docker socket is not a unix socket: $DOCKER_SOCKET_PATH" >&2
  exit 2
fi

if "$DOCKER_BIN" compose version >/dev/null 2>&1; then
  COMPOSE=("$DOCKER_BIN" compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE=(docker-compose)
elif [[ -x /Applications/Docker.app/Contents/Resources/cli-plugins/docker-compose ]]; then
  COMPOSE=(/Applications/Docker.app/Contents/Resources/cli-plugins/docker-compose)
else
  echo "Docker Compose v2 is required." >&2
  exit 2
fi

if [[ -z "${SANDBOX_CONTROLLER_TOKEN:-}" ]]; then
  if ! command -v openssl >/dev/null 2>&1; then
    echo "Set SANDBOX_CONTROLLER_TOKEN to at least 32 printable characters." >&2
    exit 2
  fi
  SANDBOX_CONTROLLER_TOKEN="$(openssl rand -hex 32)"
  export SANDBOX_CONTROLLER_TOKEN
fi

compose() {
  "${COMPOSE[@]}" -f "$COMPOSE_FILE" "$@"
}

managed_containers() {
  local namespace
  for namespace in "$TEST_NAMESPACE" "$TEST_EXPIRY_NAMESPACE"; do
    "$DOCKER_BIN" ps -aq \
      --filter "label=io.easygo.sandbox.managed=true" \
      --filter "label=io.easygo.sandbox.namespace=$namespace"
  done
}

managed_volumes() {
  local namespace
  for namespace in "$TEST_NAMESPACE" "$TEST_EXPIRY_NAMESPACE"; do
    "$DOCKER_BIN" volume ls -q \
      --filter "label=io.easygo.sandbox.managed=true" \
      --filter "label=io.easygo.sandbox.namespace=$namespace"
  done
}

force_remove_test_resources() {
  local containers volumes
  containers="$(managed_containers)"
  if [[ -n "$containers" ]]; then
    # This namespace is reserved by the integration config above.
    # shellcheck disable=SC2086
    "$DOCKER_BIN" rm -f $containers >/dev/null
  fi
  volumes="$(managed_volumes)"
  if [[ -n "$volumes" ]]; then
    # shellcheck disable=SC2086
    "$DOCKER_BIN" volume rm -f $volumes >/dev/null
  fi
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  compose stop -t 30 sandbox-controller >/dev/null 2>&1
  if "$DOCKER_BIN" image inspect easygo-agent-sandbox-controller:local >/dev/null 2>&1; then
    compose run --rm --no-deps sandbox-controller \
      -config /etc/easygo/sandbox-controller.yaml -cleanup >/dev/null 2>&1
  fi
  force_remove_test_resources
  compose down --volumes --remove-orphans >/dev/null 2>&1
  if [[ -n "$(managed_containers)" || -n "$(managed_volumes)" ]]; then
    echo "Sandbox integration cleanup left managed resources behind." >&2
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$REPO_DIR"

# Clear leftovers from an interrupted prior run in the dedicated test namespace.
compose down --volumes --remove-orphans >/dev/null 2>&1 || true
force_remove_test_resources

compose --profile sandbox-image build sandbox-runtime
compose build sandbox-controller
compose up -d --wait sandbox-controller

export EASYGO_SANDBOX_INTEGRATION_URL="http://127.0.0.1:$SANDBOX_TEST_PORT"
export EASYGO_SANDBOX_NAMESPACE="$TEST_NAMESPACE"
export EASYGO_SANDBOX_EXPIRY_NAMESPACE="$TEST_EXPIRY_NAMESPACE"
export EASYGO_SANDBOX_STRESS="${SANDBOX_RUN_STRESS:-0}"
go test ./integration/sandbox -v -count=1

if [[ "${SANDBOX_RUN_BENCHMARK:-0}" == "1" ]]; then
  benchmark_args=(
    -base-url "$EASYGO_SANDBOX_INTEGRATION_URL"
    -docker-host "$DOCKER_SOCKET_PATH"
    -namespace "$TEST_NAMESPACE"
    -warmups "${SANDBOX_BENCH_WARMUPS:-10}"
    -iterations "${SANDBOX_BENCH_ITERATIONS:-100}"
    -concurrency "${SANDBOX_BENCH_CONCURRENCY:-1,3,6}"
    -workloads "${SANDBOX_BENCH_WORKLOADS:-noop,python,go,node,cpp}"
  )
  if [[ -n "${SANDBOX_BENCH_OUTPUT:-}" ]]; then
    benchmark_args+=(-output "$SANDBOX_BENCH_OUTPUT")
  fi
  go run ./cmd/sandbox-bench "${benchmark_args[@]}"
fi
