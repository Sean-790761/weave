# Development Guide

## Project Structure

```
weave/
├── cmd/                  # Go source code
├── internal/            # Internal Go packages
├── sdk/                 # Python SDK
├── examples/            # Example agents
├── tests/              # Test files
│   ├── e2e/           # End-to-end tests
│   └── unit/          # Unit tests
├── scripts/            # Utility scripts
├── docs/              # Documentation
├── .github/           # GitHub workflows
└── Makefile           # Build automation
```

## Setting Up Development Environment

1. **Install Go dependencies**:
   ```bash
   go mod tidy
   ```

2. **Install Python dependencies**:
   ```bash
   pip install -r requirements-dev.txt
   ```

3. **Install pre-commit hooks**:
   ```bash
   pre-commit install
   ```

## Running Tests

### Go Tests
```bash
make test              # Run Go unit tests
make test-coverage     # Run with coverage report
```

### Python Tests
```bash
make test-python       # Run Python tests
make test-python-coverage  # Run with coverage
```

### End-to-End Tests
```bash
make e2e              # Run end-to-end tests
make e2e-sandbox      # Run in sandbox
```

## Code Quality

### Go Code
```bash
make fmt              # Format Go code
make lint             # Run Go linting
```

### Python Code
```bash
make fmt-py           # Format Python code
make lint-py          # Run Python linting
make check-py         # Run all Python checks
```

## Building

```bash
make build            # Build Go binary
make build-all        # Build for multiple platforms
make install          # Install binary
make install-py       # Install Python package
```

## CI/CD

The project uses GitHub Actions for CI/CD. The pipeline includes:
- Code linting (Go and Python)
- Unit tests (Go and Python)
- End-to-end tests
- Coverage reporting

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run all tests and linting
5. Submit a pull request

## Debugging

### Weave Framework
The weave framework creates temporary directories for each run. You can find these in `/tmp/weave-sandbox-*`.

### Logging
Enable verbose logging by setting environment variables:
```bash
export WEAVE_LOG_LEVEL=debug
```