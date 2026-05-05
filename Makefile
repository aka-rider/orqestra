.PHONY: build run test test-integration lint clean e2e sandbox-image sandbox-test

BINARY := orqestra

build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BINARY) ./cmd/orqestra

run: build
	./$(BINARY) $(ARGS)

test:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	#go tool cover -func=coverage.out

test-integration:
	go test -tags integration -v ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)

e2e:
	go test -tags e2e ./internal/harness/ -v -count=1 -run TestE2E -timeout 120s

sandbox-image:
	docker build \
		--build-arg UID=$(shell id -u) \
		--build-arg GID=$(shell id -g) \
		-t orqestra-sandbox:latest \
		-f build/sandbox/Dockerfile .

sandbox-test: sandbox-image
	go test ./internal/sandbox/ -v -count=1 -run Integration
