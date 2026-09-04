# weave

Agent execution framework with exit-77 loop validation for v1.1 agent contract.

[![CI](https://github.com/Sean-790761/weave/actions/workflows/ci.yml/badge.svg)](https://github.com/Sean-790761/weave/actions/workflows/ci.yml)

## Quick Start

```bash
bash test-e2e.sh
```

Requires Go 1.22+ and Python 3.

## Build & Test

```bash
# Build
make build

# Run tests
make test           # Unit tests
make e2e-sandbox   # End-to-end tests

# CI pipeline
make ci
```

## What It Proves

- **Full loop**: `ask → exit 77 → envelope → workload released → send → attempt++ → resume → replay → Succeeded`
- **I6**: State survives restart - no in-memory carryover between transitions
- **Idempotent**: Multiple questions with proper request ID handling
- **Failure modes**: `OutputMissing`, `Error`, `OutputTooLarge`, `Transient`

## Layout

```
internal/envelope/   Wire format + 4KiB budget
internal/shim/       Entrypoint wrapper
internal/model/      Task state + file store
internal/executor/   Runtime interface + Local
internal/engine/     Reconciliation engine
sdk/python/weave.py  ask() / output() / save_state()
```

## Development

```bash
make fmt       # Format code
make lint      # Check code quality
make clean     # Clean artifacts
make help      # Show all commands
```

See `CICD-SETUP.md` for CI/CD details.
