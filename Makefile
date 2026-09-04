.PHONY: all build test clean lint e2e ci install docker-build docker-test

# Variables
BINARY_NAME=weave
BUILD_DIR=bin
CMD_DIR=./cmd/weave
GO_FILES=$(shell find . -name '*.go' -type f)
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# Build
all: build

build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

build-all:
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)
	@GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_DIR)
	@GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)
	@echo "Multi-platform build complete"

install: build
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	@go install $(CMD_DIR)

# Testing
test:
	@echo "Running unit tests..."
	@go test -v -race ./...

test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

e2e: build
	@echo "Running end-to-end tests..."
	@bash test-e2e.sh

e2e-sandbox: build
	@echo "Running sandboxed end-to-end tests..."
	@bash test-e2e.sh

# Linting
lint:
	@echo "Running go vet..."
	@go vet ./...
	@echo "Checking gofmt..."
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "Go code is not formatted:"; \
		gofmt -l .; \
		exit 1; \
	fi
	@echo "Linting complete"

# CI
ci: lint test e2e-sandbox
	@echo "CI pipeline complete"

# Docker
docker-build:
	@echo "Building Docker image..."
	@docker build -t $(BINARY_NAME):$(VERSION) .
	@docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest

docker-test: docker-build
	@echo "Testing Docker image..."
	@docker run --rm $(BINARY_NAME):$(VERSION) version

# Clean
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@go clean
	@echo "Clean complete"

# Development
dev:
	@echo "Starting development mode..."
	@reflex -r '\.go$$' -s -- bash -c "make build && make test"

fmt:
	@echo "Formatting Go code..."
	@gofmt -w .
	@goimports -w .

deps:
	@echo "Updating dependencies..."
	@go mod tidy
	@go mod download

help:
	@echo "Available targets:"
	@echo "  build         - Build the binary"
	@echo "  build-all     - Build for multiple platforms"
	@echo "  install       - Install to GOPATH/bin"
	@echo "  test          - Run unit tests"
	@echo "  test-coverage - Run tests with coverage"
	@echo "  e2e           - Run end-to-end tests (test-e2e.sh)"
	@echo "  e2e-sandbox   - Run sandboxed e2e tests"
	@echo "  lint          - Run linting"
	@echo "  ci            - Run full CI pipeline"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-test   - Test Docker image"
	@echo "  clean         - Clean build artifacts"
	@echo "  dev           - Start development mode with hot reload"
	@echo "  fmt           - Format Go code"
	@echo "  deps          - Update dependencies"
	@echo "  help          - Show this help"