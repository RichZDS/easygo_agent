# One entry point for building, testing and checking the four Go modules
# (go.work), the TypeScript agent loop and the scripts. CI runs the same targets.

GO ?= $(or $(EASYGO_GO_BIN),go)
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
GOTESTFLAGS ?=
GOLANGCI_LINT ?= golangci-lint
NODE ?= node
NPM ?= npm
PRETTIER ?= npx --yes prettier@3.9.9

GO_MODULES := . packages/rpc-go services/ai-gateway services/workshop
LOOP := services/agent-loop
PRETTIER_FILES := 'scripts/**/*.mjs' 'deploy/**/*.mjs'
DOCKER_TEST_VARS := EASYGO_DOCKER_TEST_BINARY EASYGO_DOCKER_TEST_ENDPOINT EASYGO_DOCKER_TEST_IMAGE

# $(call each-module,<command>) runs <command> in every Go module and stops at the first failure.
each-module = @set -e; for m in $(GO_MODULES); do echo "==> $$m: $(1)"; (cd $$m && $(1)); done

.PHONY: build vet test test-e2e test-e2e-docker fmt lint tidy

build:
	$(call each-module,$(GO) build ./...)
	$(NPM) --prefix $(LOOP) run build

vet:
	$(call each-module,$(GO) vet ./...)

test:
	$(call each-module,$(GO) test $(GOTESTFLAGS) ./...)
	$(NPM) --prefix $(LOOP) test
	$(NODE) --test scripts/rpc-call.test.mjs

# Offline: real processes and mTLS, fixture model and CLI, no Docker.
test-e2e:
	EASYGO_GO_BIN=$(GO) $(NODE) scripts/test-services.mjs

# Real task containers on a dedicated Docker daemon (not the host socket).
test-e2e-docker: missing = $(strip $(foreach v,$(DOCKER_TEST_VARS),$(if $($(v)),,$(v))))
test-e2e-docker:
	$(if $(missing),$(error test-e2e-docker needs a dedicated Docker daemon; set $(missing)))
	@evidence=$$(mktemp -d)/evidence; echo "test-harness evidence: $$evidence"; \
	EASYGO_GO_BIN=$(GO) $(NODE) scripts/test-harness.mjs --evidence "$$evidence"
	EASYGO_GO_BIN=$(GO) $(NODE) scripts/test-platform.mjs
	EASYGO_GO_BIN=$(GO) $(NODE) scripts/test-platform-faults.mjs

fmt:
	@set -e; unformatted=$$(git ls-files -co --exclude-standard -z '*.go' | xargs -0 -r $(GOFMT) -l); \
	if [ -n "$$unformatted" ]; then echo "gofmt -l reports:"; echo "$$unformatted"; exit 1; fi
	$(PRETTIER) --check $(PRETTIER_FILES)

lint:
	$(NODE) scripts/check-cli-versions.mjs
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		rc=0; for m in $(GO_MODULES); do echo "==> $$m: golangci-lint run"; (cd $$m && $(GOLANGCI_LINT) run ./...) || rc=1; done; exit $$rc; \
	else \
		echo "skip golangci-lint: not on PATH (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)"; \
	fi

tidy:
	$(call each-module,$(GO) mod tidy)
