.PHONY: build run test lint clean

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
