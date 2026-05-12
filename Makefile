# 2026-05-11
.PHONY: build run test test-integration lint clean e2e sandbox-image sandbox-test

THIS_MAKEFILE_PATH := $(abspath $(lastword $(MAKEFILE_LIST)))
BIN_DIR := $(dir $(THIS_MAKEFILE_PATH))bin
BINARY := "$(BIN_DIR)/orqestra"

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BINARY) ./cmd/orqestra

run: build
	./$(BINARY) $(ARGS)

test:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

test-integration:
	go test -tags integration -v ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)

e2e:
	go test -tags e2e ./internal/harness/ -v -count=1 -run TestE2E -timeout 120s
