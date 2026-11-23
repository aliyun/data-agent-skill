BINARY=data-agent-mcp-server
VERSION=$(shell git describe --tags --always 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS=-trimpath

.PHONY: check-go build build-all dacli clean test

check-go:
	go version
	go list -m

build: check-go
	go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY) .
	go version -m bin/$(BINARY)

dacli: check-go
	go build $(GOFLAGS) $(LDFLAGS) -o bin/dacli ./cmd/dacli
	go version -m bin/dacli

build-all: check-go
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 .
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o bin/dacli-linux-amd64 ./cmd/dacli
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o bin/dacli-linux-arm64 ./cmd/dacli
	go version -m bin/$(BINARY)-linux-amd64 bin/$(BINARY)-linux-arm64 bin/dacli-linux-amd64 bin/dacli-linux-arm64

test: check-go
	go test ./...

clean:
	rm -rf bin/
