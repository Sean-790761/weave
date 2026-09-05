# Project Structure

This document explains the project structure following Python best practices.

## Overview

```
weave/
├── cmd/                  # Go source code for weave binary
├── pkg/                 # Public Go packages
│   └── core/           # DAG scheduler (pure function, no IO)
├── internal/            # Internal Go packages
├── sdk/                 # Python SDK for weave agents
├── examples/            # Example agent implementations
├── tests/              # Test files
│   ├── e2e/           # End-to-end tests
│   └── unit/          # Unit tests
├── scripts/            # Utility scripts
├── docs/              # Documentation
├── .github/           # GitHub workflows
└── Makefile           # Build automation
```

## Directory Details

### cmd/
Contains the main Go application entry point.
- `cmd/weave/main.go` - Main binary for the weave framework

### pkg/
Public Go packages, importable from outside the module.
- `pkg/core/` - The DAG scheduler: topology parsing/validation, `{{ agent.output.key }}`
  rendering, and `Decide(Topology, Observed) -> []Action`. A pure function: no IO, no
  clock, no goroutines, standard library only.

### internal/
Internal Go packages not intended for external use.
- `internal/envelope/` - Wire format and envelope handling
- `internal/model/` - Task state and file store
- `internal/executor/` - Runtime interface and local executor
- `internal/engine/` - Reconciliation engine (one task)
- `internal/run/` - Run driver: wires pkg/core to the engine, one workspace per task
- `internal/shim/` - Entrypoint wrapper

### sdk/
Python SDK for writing weave agents.
- `sdk/python/weave.py` - Core SDK implementation

### examples/
Example agent implementations.
- `examples/reviewer.py` - Example reviewer agent with human checkpoints

### tests/
Test files for both Go and Python components.
- `tests/e2e/test_e2e.py` - End-to-end tests
- `tests/unit/` - Unit tests (to be implemented)

### scripts/
Utility scripts for development and deployment.
- `scripts/run_examples.py` - Script to run examples

### docs/
Documentation files.
- `docs/development.md` - Development guide
- `docs/PROJECT_STRUCTURE.md` - This file

### Configuration Files

- `pyproject.toml` - Python project configuration
- `setup.py` - Python package setup
- `requirements.txt` - Python dependencies
- `requirements-dev.txt` - Development dependencies
- `.gitignore` - Git ignore patterns
- `.pre-commit-config.yaml` - Pre-commit hooks
- `Makefile` - Build automation

## Best Practices

### Go Code
- Follow standard Go conventions
- Use proper error handling
- Keep packages focused and small

### Python Code
- Follow PEP 8 style guide
- Use type hints where appropriate
- Write comprehensive docstrings
- Use black for formatting
- Run mypy for type checking

### Testing
- Write tests for all public APIs
- Use pytest for Python tests
- Maintain good test coverage
- Include both unit and integration tests

### Documentation
- Keep README.md updated
- Document all public APIs
- Include examples in documentation
- Update development guide when changing processes

## Build and Deployment

The project uses Make for build automation and GitHub Actions for CI/CD.

### Build Commands
- `make build` - Build Go binary
- `make install` - Install Go binary
- `make install-py` - Install Python package

### Test Commands
- `make test` - Run Go tests
- `make test-python` - Run Python tests
- `make e2e` - Run end-to-end tests

### Code Quality
- `make lint` - Run Go linting
- `make lint-py` - Run Python linting
- `make fmt` - Format Go code
- `make fmt-py` - Format Python code