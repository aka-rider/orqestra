.PHONY: build run test lint clean sandbox-image sandbox-test

BINARY := orqestra

build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BINARY) ./cmd/orqestra

run: build
	./$(BINARY) $(ARGS)

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY)

sandbox-image:
	docker build -t orqestra-sandbox:latest -f build/sandbox/Dockerfile .

sandbox-test: sandbox-image
	go test ./internal/sandbox/ -v -count=1 -run Integration
