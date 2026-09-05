# weave

Agent execution framework: a DAG of agents that can pause mid-run to ask a
human and then carry on — without a runtime that can actually pause anything.

[![CI](https://github.com/Sean-790761/weave/actions/workflows/ci.yml/badge.svg)](https://github.com/Sean-790761/weave/actions/workflows/ci.yml)

## Quick Start

```bash
make e2e
```

Requires Go 1.25+ and Python 3. The Python targets create and maintain their
own virtualenv (`.venv`), so a fresh clone needs no `pip install`; pass
`SYSTEM_PYTHON=1` to use the ambient interpreter instead.

## Build & Test

```bash
make build         # Build the binary
make test          # Go unit + integration tests (race detector on)
make test-python   # Python unit tests
make e2e           # End-to-end tests: pytest driving the real binary
make ci            # Full pipeline: lint, tests, e2e
```

## What It Proves

- **Full loop**: `ask → exit 77 → envelope → workload released → send → attempt++ → resume → replay → Succeeded`
- **I6**: state survives restart — every transition is derived from disk, at the
  run level as well as the task level
- **DAG**: a declared output reaches the next agent's argv and env; a question
  parks its own branch and no other; a failure never starts the branch below it
- **Failure modes**: `OutputMissing`, `Error`, `OutputTooLarge`, `Transient`,
  `ContractViolation`

## Layout

```
pkg/core/            DAG scheduler: topology, validation, {{ }} rendering, Decide()
internal/run/        Run driver: wires core to the engine, one workspace per task
internal/engine/     Reconciliation for a single task
internal/executor/   Runtime interface + Local
internal/model/      Task state + file store
internal/envelope/   Wire format + 4KiB budget
internal/shim/       Entrypoint wrapper
sdk/python/weave.py  ask() / output() / save_state()
```

The split between the top two is deliberate. `pkg/core` decides *what* should
happen: a pure function over (topology, observed tasks), with no IO, no clock
and no Kubernetes types, so the scheduling rules can be pinned by table tests.
`internal/run` performs those decisions and owns every side effect. See
`ARCHITECTURE.md` for the as-built picture and `DESIGN-v1.1.md` for the design
it is converging on.

## Development

```bash
make fmt       # Format Go code
make lint      # Run golangci-lint
make clean     # Clean artifacts
make help      # Show all commands
```

## Code Quality

Go code is checked with `golangci-lint`, configured in `.golangci.yml` and run
on every push and pull request:

```bash
# Install locally
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin latest

make lint
```

Python code is checked with `flake8` + `mypy` (`make lint-py`) and formatted
with `black` (`make fmt-py`).
