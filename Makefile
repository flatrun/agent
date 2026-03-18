.PHONY: help build run test test-unit test-e2e test-e2e-docker test-e2e-setup test-e2e-run test-e2e-short test-e2e-cleanup test-e2e-security test-e2e-security-setup test-e2e-security-cleanup test-e2e-lua test-e2e-lua-setup test-e2e-lua-cleanup test-coverage clean deps lint lint-install fmt vet

BINARY_NAME=flatrun-agent
VERSION?=$(shell cat VERSION 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-X github.com/flatrun/agent/pkg/version.Version=$(VERSION) -X github.com/flatrun/agent/pkg/version.BuildTime=$(BUILD_TIME) -X github.com/flatrun/agent/pkg/version.GitCommit=$(GIT_COMMIT)"

E2E_API_URL?=http://localhost:18090/api
E2E_DEPLOYMENTS_PATH?=/tmp/flatrun-e2e-deployments
E2E_SECURITY_PATH?=/tmp/flatrun-e2e-security
E2E_LUA_PATH?=/tmp/flatrun-e2e-lua

help:
	@echo "FlatRun Agent - Build commands"
	@echo ""
	@echo "make deps               - Install dependencies"
	@echo "make build              - Build binary"
	@echo "make run                - Run in development mode"
	@echo "make test               - Run unit tests"
	@echo "make test-unit          - Run unit tests only"
	@echo "make test-e2e           - Run E2E tests (requires running agent)"
	@echo "make test-e2e-docker    - Run full E2E tests in Docker (setup + run + cleanup)"
	@echo "make test-e2e-setup     - Start E2E Docker test environment"
	@echo "make test-e2e-run       - Run E2E tests against Docker environment"
	@echo "make test-e2e-short     - Run E2E tests (skip long-running)"
	@echo "make test-e2e-cleanup   - Clean up Docker test environment"
	@echo "make test-e2e-security  - Run security E2E tests (static configs)"
	@echo "make test-e2e-lua       - Run Lua/OpenResty E2E tests (realtime capture)"
	@echo "make test-coverage      - Run tests with coverage report"
	@echo "make lint               - Run golangci-lint"
	@echo "make lint-install       - Install golangci-lint"
	@echo "make fmt                - Format code with gofmt"
	@echo "make vet                - Run go vet"
	@echo "make clean              - Clean build artifacts"

deps:
	go mod download
	go mod tidy

build: deps
	go build $(LDFLAGS) -o $(BINARY_NAME) cmd/agent/main.go

run: deps
	go run cmd/agent/main.go --config config.example.yml

test: test-unit

test-unit:
	@echo "Running unit tests..."
	go test -v ./templates/...
	go test -v ./internal/...
	go test -v ./pkg/...

test-e2e:
	@echo "Running E2E tests (requires running agent on port 8090)..."
	go test -v ./test/e2e/...

test-e2e-setup:
	@echo "Setting up E2E Docker test environment..."
	@mkdir -p $(E2E_DEPLOYMENTS_PATH)/nginx/conf.d
	@mkdir -p $(E2E_DEPLOYMENTS_PATH)/nginx/certs
	@chmod -R 777 $(E2E_DEPLOYMENTS_PATH) 2>/dev/null || true
	@docker network create proxy 2>/dev/null || true
	@docker network create database 2>/dev/null || true
	cd test/e2e && docker compose -f docker-compose.test.yml up -d --build
	@echo "Waiting for services to be healthy..."
	@timeout 120 bash -c 'until docker exec flatrun-e2e-agent wget -q -O /dev/null http://127.0.0.1:8090/api/health 2>/dev/null; do sleep 2; done' || (echo "Agent failed to start" && exit 1)
	@chmod -R 777 $(E2E_DEPLOYMENTS_PATH) 2>/dev/null || true
	@echo "E2E environment ready at $(E2E_API_URL)"

test-e2e-run:
	@echo "Running E2E tests against Docker environment..."
	FLATRUN_API_URL=$(E2E_API_URL) FLATRUN_DEPLOYMENTS_PATH=$(E2E_DEPLOYMENTS_PATH) go test -v -timeout 10m ./test/e2e/...

test-e2e-docker: test-e2e-setup test-e2e-run
	@echo "E2E tests completed. Run 'make test-e2e-cleanup' to remove containers."

test-e2e-docker-full: test-e2e-setup test-e2e-run test-e2e-cleanup
	@echo "E2E tests completed and cleaned up."

test-e2e-short:
	@echo "Running E2E tests (short mode)..."
	FLATRUN_API_URL=$(E2E_API_URL) FLATRUN_DEPLOYMENTS_PATH=$(E2E_DEPLOYMENTS_PATH) go test -v -short -timeout 5m ./test/e2e/...

test-e2e-cleanup:
	@echo "Cleaning up all E2E test environments..."
	cd test/e2e && docker compose -f docker-compose.test.yml down -v --remove-orphans 2>/dev/null || true
	cd test/e2e && docker compose -f docker-compose.security.yml down -v --remove-orphans 2>/dev/null || true
	cd test/e2e && docker compose -f docker-compose.lua.yml down -v --remove-orphans 2>/dev/null || true
	@docker network rm proxy 2>/dev/null || true
	@docker network rm database 2>/dev/null || true
	@rm -rf $(E2E_DEPLOYMENTS_PATH)/* 2>/dev/null || true
	@rm -rf $(E2E_SECURITY_PATH)/* 2>/dev/null || true
	@rm -rf $(E2E_LUA_PATH)/* 2>/dev/null || true
	@echo "Cleanup complete"

test-e2e-security-setup:
	@echo "Setting up Security E2E test environment..."
	@mkdir -p $(E2E_SECURITY_PATH)/nginx/conf.d
	@chmod -R 777 $(E2E_SECURITY_PATH) 2>/dev/null || true
	@docker network create proxy 2>/dev/null || true
	cd test/e2e && docker compose -f docker-compose.security.yml up -d --build
	@echo "Waiting for services to be healthy..."
	@timeout 120 bash -c 'until docker exec flatrun-e2e-security-agent wget -q -O /dev/null http://127.0.0.1:8090/api/health 2>/dev/null; do sleep 2; done' || (echo "Security agent failed to start" && exit 1)
	@chmod -R 777 $(E2E_SECURITY_PATH) 2>/dev/null || true
	@echo "Security E2E environment ready"

test-e2e-security-cleanup:
	@echo "Cleaning up Security E2E test environment..."
	cd test/e2e && docker compose -f docker-compose.security.yml down -v --remove-orphans 2>/dev/null || true
	@rm -rf $(E2E_SECURITY_PATH)/* 2>/dev/null || true
	@echo "Security cleanup complete"

test-e2e-security:
	@echo "Running Security E2E tests..."
	FLATRUN_SECURITY_TEST=true go test -v -timeout 5m ./test/e2e/... -run TestSecurityConfigFilesCreated

test-e2e-lua-setup:
	@echo "Setting up Lua/OpenResty E2E test environment..."
	@mkdir -p $(E2E_LUA_PATH)/nginx/conf.d
	@chmod -R 777 $(E2E_LUA_PATH) 2>/dev/null || true
	@docker network create proxy 2>/dev/null || true
	cd test/e2e && docker compose -f docker-compose.lua.yml up -d --build
	@echo "Waiting for services to be healthy..."
	@timeout 120 bash -c 'until docker exec flatrun-e2e-lua-agent wget -q -O /dev/null http://127.0.0.1:8090/api/health 2>/dev/null; do sleep 2; done' || (echo "Lua agent failed to start" && exit 1)
	@chmod -R 777 $(E2E_LUA_PATH) 2>/dev/null || true
	@echo "Lua/OpenResty E2E environment ready"

test-e2e-lua-cleanup:
	@echo "Cleaning up Lua/OpenResty E2E test environment..."
	cd test/e2e && docker compose -f docker-compose.lua.yml down -v --remove-orphans 2>/dev/null || true
	@rm -rf $(E2E_LUA_PATH)/* 2>/dev/null || true
	@echo "Lua cleanup complete"

test-e2e-lua:
	@echo "Running Lua/OpenResty E2E tests..."
	FLATRUN_LUA_TEST=true go test -v -timeout 5m ./test/e2e/... -run TestLuaRealtimeCapture

test-coverage:
	@echo "Running tests with coverage..."
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

test-all: test-unit test-e2e

lint:
	@command -v golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || \
	(test -x "$$(go env GOPATH)/bin/golangci-lint" && "$$(go env GOPATH)/bin/golangci-lint" run ./... || \
	(echo "golangci-lint not found. Run 'make lint-install' first." && exit 1))

lint-install:
	@echo "Installing golangci-lint..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY_NAME)
	go clean

dev:
	go run cmd/agent/main.go --config config.example.yml
