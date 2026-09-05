.PHONY: all build build-all install test test-coverage test-python test-python-coverage \
	e2e e2e-sandbox lint lint-py fmt fmt-py check-py ci ci-fast clean clean-venv dev deps \
	venv install-py docker-build docker-test help

# Variables
BINARY_NAME=weave
BUILD_DIR=bin
CMD_DIR=./cmd/weave
GO_FILES=$(shell find . -name '*.go' -type f)
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# ---------------------------------------------------------------------------
# Python toolchain
#
# Every Python target runs out of a project-local virtualenv ($(VENV)) that is
# created and kept in sync with requirements-dev.txt automatically. A fresh
# clone can run `make e2e` with no manual `pip install`, and the result no
# longer depends on whichever python3 happens to be first on PATH (a Homebrew
# 3.14 without pytest, for example).
#
#   make e2e                   # bootstraps .venv if needed, then runs pytest
#   make SYSTEM_PYTHON=1 e2e   # use the ambient python3 (CI images, nix, ...)
#   make venv                  # just create/refresh the venv
#   make clean-venv            # throw it away
# ---------------------------------------------------------------------------
PYTHON     ?= python3
VENV       ?= .venv
VENV_PY     = $(VENV)/bin/python
VENV_STAMP  = $(VENV)/.deps-installed
PY_REQS     = requirements-dev.txt

ifeq ($(SYSTEM_PYTHON),1)
PY      := $(PYTHON)
PY_DEPS :=
else
PY      := $(VENV_PY)
PY_DEPS := $(VENV_STAMP)
endif

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

test-python: $(PY_DEPS)
	@echo "Running Python unit tests..."
	@$(PY) -m pytest tests/unit

test-python-coverage: $(PY_DEPS)
	@echo "Running Python tests with coverage..."
	@$(PY) -m pytest tests/ --cov=sdk --cov-report=html --cov-report=term-missing

e2e: build $(PY_DEPS)
	@echo "Running end-to-end tests..."
	@$(PY) -m pytest tests/e2e

e2e-sandbox: e2e
	@echo "Sandboxed e2e is the same path as e2e (local executor runs in a temp dir)"

# Python environment
venv: $(VENV_STAMP)

$(VENV_STAMP): $(PY_REQS)
	@command -v $(PYTHON) >/dev/null 2>&1 || { echo "$(PYTHON) not found on PATH"; exit 1; }
	@test -x $(VENV_PY) || { echo "Creating virtualenv in $(VENV)..."; $(PYTHON) -m venv $(VENV); }
	@echo "Installing Python dev dependencies into $(VENV)..."
	@$(VENV_PY) -m pip install --quiet --upgrade pip
	@$(VENV_PY) -m pip install --quiet -r $(PY_REQS)
	@touch $@

# Linting
lint:
	@echo "Running golangci-lint..."
	@if ! command -v golangci-lint &> /dev/null; then \
		echo "golangci-lint not found. Installing..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin latest; \
	fi
	@golangci-lint run --timeout=5m --config=.golangci.yml
	@echo "Linting complete"

# CI
ci: lint test test-python lint-py e2e-sandbox
	@echo "CI pipeline complete"

ci-fast: lint test e2e-sandbox
	@echo "Fast CI pipeline complete"

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
	@rm -rf .pytest_cache .mypy_cache htmlcov
	@rm -f coverage.out coverage.html .coverage
	@go clean
	@echo "Clean complete (run 'make clean-venv' to also drop $(VENV))"

clean-venv:
	@echo "Removing $(VENV)..."
	@rm -rf $(VENV)

# Development
dev:
	@echo "Starting development mode..."
	@reflex -r '\.go$$' -s -- bash -c "make build && make test"

fmt:
	@echo "Formatting Go code..."
	@if ! command -v goimports &> /dev/null; then \
		echo "goimports not found. Installing..."; \
		go install golang.org/x/tools/cmd/goimports@latest; \
	fi
	@gofmt -w .
	@goimports -w .

deps: venv
	@echo "Updating Go dependencies..."
	@go mod tidy
	@go mod download

install-py: $(PY_DEPS)
	@echo "Installing Python package into $(VENV)..."
	@$(PY) -m pip install -e .

lint-py: $(PY_DEPS)
	@echo "Running Python linting..."
	@$(PY) -m flake8 sdk/ tests/ examples/
	@$(PY) -m mypy sdk/ examples/

fmt-py: $(PY_DEPS)
	@echo "Formatting Python code..."
	@$(PY) -m black sdk/ tests/ examples/

# Format first, then lint, so the linter sees the formatted code.
check-py: fmt-py lint-py
	@echo "Python code quality check complete"

help:
	@echo "Available targets:"
	@echo "  build         - Build the binary"
	@echo "  build-all     - Build for multiple platforms"
	@echo "  install       - Install to GOPATH/bin"
	@echo "  test          - Run unit tests"
	@echo "  test-coverage - Run tests with coverage"
	@echo "  test-python   - Run Python unit tests"
	@echo "  test-python-coverage - Run Python tests with coverage"
	@echo "  e2e           - Run end-to-end tests"
	@echo "  e2e-sandbox   - Run sandboxed e2e tests"
	@echo "  lint          - Run linting"
	@echo "  ci            - Run full CI pipeline"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-test   - Test Docker image"
	@echo "  clean         - Clean build artifacts"
	@echo "  clean-venv    - Remove the Python virtualenv ($(VENV))"
	@echo "  dev           - Start development mode with hot reload"
	@echo "  fmt           - Format Go code"
	@echo "  deps          - Update dependencies (Go modules + Python venv)"
	@echo "  venv          - Create/refresh the Python virtualenv ($(VENV))"
	@echo "  install-py    - Install Python package (editable) into the venv"
	@echo "  lint-py       - Run Python linting"
	@echo "  fmt-py        - Format Python code"
	@echo "  help          - Show this help"
	@echo ""
	@echo "Python targets bootstrap $(VENV) automatically."
	@echo "Set SYSTEM_PYTHON=1 to use the ambient python3 instead, e.g."
	@echo "  make SYSTEM_PYTHON=1 e2e"