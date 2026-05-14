# 2026-05-11
.PHONY: build run test test-unit test-integration lint clean e2e sandbox-image test-sandbox

THIS_MAKEFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
BIN_DIR := $(dir $(THIS_MAKEFILE_PATH))bin
BINARY := "$(BIN_DIR)/orqestra"

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BINARY) ./cmd/orqestra

run: build
	$(BINARY) $(ARGS)

# Test tiers:
#   make test             — unit tests (no external deps, fast, run on every commit)
#   make test-unit        — alias for test
#   make test-integration — integration tests (requires git + go build, run pre-merge)
#   make test-sandbox     — macOS sandbox tests (requires sandbox-exec, darwin only)
#   make e2e              — end-to-end tests (requires real claude CLI + API)

test:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

test-unit:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

# Requires: git in PATH, go build access. Runs worktree lifecycle and CLI smoke tests.
test-integration:
	go test -tags integration -race -v ./...

# Requires: macOS with sandbox-exec.
test-sandbox:
	go test -tags 'darwin integration' -race -v ./internal/sandbox/...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)

e2e:
	go test -tags e2e ./internal/harness/ -v -count=1 -run TestE2E -timeout 120s
