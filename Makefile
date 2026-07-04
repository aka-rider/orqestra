# 2026-05-11
.PHONY: build run test test-unit test-integration lint clean e2e sandbox-image test-sandbox test-fuzzy

THIS_MAKEFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
ORQESTRA := "$(dir $(THIS_MAKEFILE_PATH))bin/orqestra"


build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(ORQESTRA) ./cmd/orqestra

run: build
	$(ORQESTRA) $(ARGS)

# Test tiers:
#   make test             — unit tests (no external deps, fast, run on every commit)
#   make test-integration — integration tests (requires git + go build, run pre-merge)
#   make test-sandbox     — macOS sandbox tests (requires sandbox-exec, darwin only)
#   make e2e              — end-to-end tests (requires real claude CLI + API)

# `make test` runs through cmd/qarun: the suite under a hard wall-clock deadline
# (per-package -timeout + outer QA_DEADLINE) so a hang is a bounded NO-VERDICT,
# never an indefinite hang. On success it prints a QA-ATTEST line — the only
# valid proof the suite passed. Never report green without that line.
test:
	go run ./cmd/qarun

# Requires: git in PATH, go build access. Runs worktree lifecycle and CLI smoke tests.
test-integration:
	go test -tags integration -race -v -timeout 120s ./...

# Requires: macOS with sandbox-exec.
test-sandbox:
	go test -tags 'darwin integration' -race -v ./internal/harness/...

# Live e2e (real claude + API) is NOT yet implemented — there is no TestE2E, so
# the old `go test -run TestE2E` recipe passed by vacuity (zero tests = green).
# Until a real L3 lane exists, this target must not masquerade as coverage.
test-e2e:
	@echo "test-e2e: no live e2e (L3) tests implemented yet — placeholder, not coverage."

lint:
	go vet ./...

# Fuzz all targets for T (default 3m) each, in parallel.
# Requires: -tags=fuzz build tag; each target runs in a separate go test process.
# Usage: make test-fuzzy T=3m
T ?= 3m
test-fuzzy:
	@go test -run=^$$ -fuzz=FuzzParseStreamLines      -fuzztime=$(T) -tags=fuzz ./internal/harness/ & p1=$$!; \
	 go test -run=^$$ -fuzz=FuzzTUIInput              -fuzztime=$(T) -tags=fuzz ./internal/tui/     & p2=$$!; \
	 go test -run=^$$ -fuzz=FuzzParseValidationOutput -fuzztime=$(T) -tags=fuzz ./internal/agent/   & p3=$$!; \
	 go test -run=^$$ -fuzz=FuzzParseCommitMessage    -fuzztime=$(T) -tags=fuzz ./internal/agent/   & p4=$$!; \
	 rc=0; wait $$p1 || rc=1; wait $$p2 || rc=1; wait $$p3 || rc=1; wait $$p4 || rc=1; exit $$rc

test-all: test test-integration test-sandbox test-e2e lint


clean:
	rm -rf $(dir $(THIS_MAKEFILE_PATH))bin


