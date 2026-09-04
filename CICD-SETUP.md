# CI/CD Setup

## Workflows

**CI Pipeline** (`.github/workflows/ci.yml`)
- Multi-platform builds
- Unit tests with race detection
- E2E validation
- Code quality checks
- Artifact uploads

**Release Pipeline** (`.github/workflows/release.yml`)
- Multi-platform binary builds
- SHA256 checksums
- GitHub releases
- Docker image publishing

## Testing

**Sandboxed E2E** (`test-e2e.sh`)
```bash
bash test-e2e.sh  # Isolated, auto-cleanup
make e2e-sandbox
```

## Build & Test

```bash
make build          # Build binary
make test           # Unit tests
make ci            # Full CI pipeline
make lint          # Code quality
make fmt           # Format code
make help          # All commands
```

## CI Flow

1. Developer pushes changes
2. GitHub CI runs automatically
3. Tests build → test → lint → upload artifacts
4. Tag release triggers release pipeline

## Release

```bash
git tag v1.0.0
git push --tags
```

Creates GitHub release with:
- Multi-platform binaries
- SHA256 checksums
- Docker images

## Files Created

```
.github/workflows/ci.yml
.github/workflows/release.yml
test-e2e.sh
Makefile
Dockerfile
.dockerignore
go.sum
```

---

**Status**: ✅ Ready for GitHub integration