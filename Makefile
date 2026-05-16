# 2026-05-11
.PHONY: build run test test-unit test-integration lint clean e2e sandbox-image test-sandbox

THIS_MAKEFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
ORQESTRA := "$(dir $(THIS_MAKEFILE_PATH))orqestra"


build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(ORQESTRA) ./cmd/orqestra

run: build
	$(ORQESTRA) $(ARGS)

# Test tiers:
#   make test             — unit tests (no external deps, fast, run on every commit)
#   make test-integration — integration tests (requires git + go build, run pre-merge)
#   make test-sandbox     — macOS sandbox tests (requires sandbox-exec, darwin only)
#   make e2e              — end-to-end tests (requires real claude CLI + API)

test:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

# Requires: git in PATH, go build access. Runs worktree lifecycle and CLI smoke tests.
test-integration:
	go test -tags integration -race -v ./...

# Requires: macOS with sandbox-exec.
test-sandbox:
	go test -tags 'darwin integration' -race -v ./internal/sandbox/...

test-e2e:
	go test -tags e2e ./internal/harness/ -v -count=1 -run TestE2E -timeout 120s

lint:
	go vet ./...

test-all: test test-integration test-sandbox test-e2e lint


clean:
	rm -f $(BINARY)

