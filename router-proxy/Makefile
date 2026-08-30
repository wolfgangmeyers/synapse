.PHONY: build test check coverage build-router run-router

ROUTER_BINARY ?= /tmp/mecha-mailbox-router
ROUTER_DATABASE ?= $(HOME)/.mecha-developer/router.sqlite
ROUTER_LOG ?= $(HOME)/.mecha-developer/router.jsonl
ROUTER_HOST ?= 127.0.0.1
ROUTER_PORT ?= 8765
ROUTER_MAX_BATCH_CHARS ?= 4096

build:
	go build ./...

build-router:
	go build -o "$(ROUTER_BINARY)" ./cmd/mailbox-router

run-router: build-router
	exec "$(ROUTER_BINARY)" --database "$(ROUTER_DATABASE)" --host "$(ROUTER_HOST)" --port "$(ROUTER_PORT)" --log "$(ROUTER_LOG)" --max-batch-chars "$(ROUTER_MAX_BATCH_CHARS)"

test:
	go test ./...

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./...

coverage:
	go test -coverprofile=/tmp/mecha-router-proxy-coverage.out -covermode=atomic ./...
	go tool cover -func=/tmp/mecha-router-proxy-coverage.out
