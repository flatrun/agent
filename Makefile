.PHONY: help build run test clean deps lint lint-install fmt vet

BINARY_NAME=flatrun-agent
VERSION?=dev
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

help:
	@echo "FlatRun Agent - Build commands"
	@echo ""
	@echo "make deps         - Install dependencies"
	@echo "make build        - Build binary"
	@echo "make run          - Run in development mode"
	@echo "make test         - Run tests"
	@echo "make lint         - Run golangci-lint"
	@echo "make lint-install - Install golangci-lint"
	@echo "make fmt          - Format code with gofmt"
	@echo "make vet          - Run go vet"
	@echo "make clean        - Clean build artifacts"

deps:
	go mod download
	go mod tidy

build: deps
	go build $(LDFLAGS) -o $(BINARY_NAME) cmd/agent/main.go

run: deps
	go run cmd/agent/main.go --config config.example.yml

test:
	go test -v ./...

lint:
	@command -v golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || \
	(test -x "$$(go env GOPATH)/bin/golangci-lint" && "$$(go env GOPATH)/bin/golangci-lint" run ./... || \
	(echo "golangci-lint not found. Run 'make lint-install' first." && exit 1))

lint-install:
	@echo "Installing golangci-lint..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY_NAME)
	go clean

dev:
	go run cmd/agent/main.go --config config.example.yml
