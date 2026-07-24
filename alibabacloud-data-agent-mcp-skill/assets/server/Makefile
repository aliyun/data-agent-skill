BINARY=data-agent-mcp-server
VERSION=$(shell git describe --tags --always 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"
GOFLAGS=-trimpath

.PHONY: build build-all clean test

build:
	go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY) .

build-all:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 .
	upx --best bin/$(BINARY)-linux-amd64 bin/$(BINARY)-linux-arm64 || true

test:
	go test ./...

clean:
	rm -rf bin/
