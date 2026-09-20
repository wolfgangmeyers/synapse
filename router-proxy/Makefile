.PHONY: build test check coverage build-router build-router-local run-router

ROUTER_BINARY ?= $(HOME)/.mecha-developer/bin/mecha-mailbox-router
ROUTER_SOURCE_COMMIT ?= $(shell git -C .. rev-parse HEAD)
ROUTER_SOURCE_TREE ?= $(shell if test -z "$$(git -C .. status --porcelain --untracked-files=all)"; then printf clean; else printf dirty; fi)
ROUTER_DATABASE ?= $(HOME)/.mecha-developer/router.sqlite
ROUTER_LOG ?= $(HOME)/.mecha-developer/router.jsonl
ROUTER_HOST ?= 127.0.0.1
ROUTER_PORT ?= 8765
ROUTER_MAX_BATCH_CHARS ?= 4096
ROUTER_AUDIT_SUPERVISOR_TARGET ?= supervisor
ROUTER_AUDIT_TMUX_SESSION ?= mecha
ROUTER_POLICY_TIMEOUT ?= 5s
ROUTER_MANAGED ?= true
ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH ?= false
ROUTER_MANAGED_PI ?=
ROUTER_MANAGED_HOME ?=
ROUTER_MANAGED_PI_AGENT_DIR ?=
ROUTER_MANAGED_SESSION_ROOT ?=
ROUTER_MANAGED_MANIFEST ?=
ROUTER_MANAGED_MECHA_RPC ?=
ROUTER_MANAGED_PI_SUBAGENTS ?=
ROUTER_MANAGED_CURSOR_EXTENSION ?=
ROUTER_MANAGED_AUTH_HELPER ?=
ROUTER_MANAGED_RUNTIME_ROOT ?=
ROUTER_MANAGED_CWD_ROOTS ?=
ROUTER_MANAGED_QUIET_RECIPIENT ?=
ROUTER_MANAGED_UI_DIST ?=

build:
	go build ./...

build-router:
	@test "$(ROUTER_SOURCE_TREE)" = clean || { printf '%s\n' "Refusing to deploy a router binary from a dirty source tree." >&2; exit 1; }
	@mkdir -p "$(dir $(ROUTER_BINARY))"
	go build -trimpath -buildvcs=false -ldflags "-X main.buildSourceCommit=$(ROUTER_SOURCE_COMMIT) -X main.buildSourceTree=$(ROUTER_SOURCE_TREE)" -o "$(ROUTER_BINARY)" ./cmd/mailbox-router

# Local recovery must be possible while feature work is in progress. The
# runtime health endpoint records the dirty source state instead of pretending
# this is a clean deploy artifact.
build-router-local:
	@mkdir -p "$(dir $(ROUTER_BINARY))"
	go build -trimpath -buildvcs=false -ldflags "-X main.buildSourceCommit=$(ROUTER_SOURCE_COMMIT) -X main.buildSourceTree=$(ROUTER_SOURCE_TREE)" -o "$(ROUTER_BINARY)" ./cmd/mailbox-router

run-router: build-router-local
	exec "$(ROUTER_BINARY)" --database "$(ROUTER_DATABASE)" --host "$(ROUTER_HOST)" --port "$(ROUTER_PORT)" --log "$(ROUTER_LOG)" --max-batch-chars "$(ROUTER_MAX_BATCH_CHARS)" --audit-supervisor-target "$(ROUTER_AUDIT_SUPERVISOR_TARGET)" --audit-tmux-session "$(ROUTER_AUDIT_TMUX_SESSION)" --policy-timeout "$(ROUTER_POLICY_TIMEOUT)" --managed="$(ROUTER_MANAGED)" --managed-allow-launch-recipe-mismatch="$(ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH)" --managed-pi "$(ROUTER_MANAGED_PI)" --managed-home "$(ROUTER_MANAGED_HOME)" --managed-pi-agent-dir "$(ROUTER_MANAGED_PI_AGENT_DIR)" --managed-session-root "$(ROUTER_MANAGED_SESSION_ROOT)" --managed-manifest "$(ROUTER_MANAGED_MANIFEST)" --managed-mecha-rpc "$(ROUTER_MANAGED_MECHA_RPC)" --managed-pi-subagents "$(ROUTER_MANAGED_PI_SUBAGENTS)" --managed-cursor-extension "$(ROUTER_MANAGED_CURSOR_EXTENSION)" --managed-auth-helper "$(ROUTER_MANAGED_AUTH_HELPER)" --managed-runtime-root "$(ROUTER_MANAGED_RUNTIME_ROOT)" --managed-cwd-roots "$(ROUTER_MANAGED_CWD_ROOTS)" --managed-quiet-recipient "$(ROUTER_MANAGED_QUIET_RECIPIENT)" --managed-ui-dist "$(ROUTER_MANAGED_UI_DIST)"

test:
	go test ./...

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./...

coverage:
	go test -coverprofile=/tmp/mecha-router-proxy-coverage.out -covermode=atomic ./...
	go tool cover -func=/tmp/mecha-router-proxy-coverage.out
