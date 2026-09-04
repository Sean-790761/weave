#!/usr/bin/env bash
# End-to-end test runner with sandbox isolation
# Creates a temporary workspace for testing and cleans up automatically

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Setup
cd "$(dirname "$0")"
export HOME="${HOME:-/tmp}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
SANDBOX_DIR="/tmp/weave-sandbox-${TIMESTAMP}"

log_info "Creating sandbox: ${SANDBOX_DIR}"
mkdir -p "${SANDBOX_DIR}"

# Cleanup on exit
cleanup() {
    EXIT_CODE=$?
    if [ -d "${SANDBOX_DIR}" ]; then
        log_info "Cleaning up sandbox: ${SANDBOX_DIR}"
        rm -rf "${SANDBOX_DIR}"
    fi
    exit $EXIT_CODE
}
trap cleanup EXIT

# Build binary
log_info "Building weave binary..."
go build -o bin/weave ./cmd/weave

if [ ! -f bin/weave ]; then
    log_error "Build failed - binary not found"
    exit 1
fi

W="$PWD/bin/weave"
AGENT="$PWD/examples/reviewer.py"

# Helper function to get current request ID
rid() {
    "$W" status --dir "$SANDBOX_DIR" --json | python3 -c \
      'import json,sys;print(json.load(sys.stdin)["status"]["userInput"]["requestId"])' 2>/dev/null || echo ""
}

# Test counter
TESTS_PASSED=0
TESTS_FAILED=0

run_test() {
    local test_name="$1"
    shift
    local command=("$@")

    log_info "Running test: ${test_name}"
    if "${command[@]}" > /dev/null 2>&1; then
        log_info "✓ ${test_name} passed"
        ((TESTS_PASSED++))
        return 0
    else
        log_error "✗ ${test_name} failed"
        ((TESTS_FAILED++))
        return 1
    fi
}

run_test_with_output() {
    local test_name="$1"
    shift
    local command=("$@")

    log_info "Running test: ${test_name}"
    if "${command[@]}" 2>&1 | tee "${SANDBOX_DIR}/test-output.log" | grep -q "Succeeded"; then
        log_info "✓ ${test_name} passed"
        ((TESTS_PASSED++))
        return 0
    else
        log_error "✗ ${test_name} failed"
        cat "${SANDBOX_DIR}/test-output.log" | tail -20
        ((TESTS_FAILED++))
        return 1
    fi
}

# Test 1: Initial submission
log_info "=== Test 1: Initial submission ==="
if "$W" run --dir "${SANDBOX_DIR}" --agent reviewer \
  --output score:required --output summary:required \
  -- python3 "$AGENT" | grep -q "WaitingForUserInput"; then
    log_info "✓ Initial submission works"
    ((TESTS_PASSED++))
else
    log_error "✗ Initial submission failed"
    ((TESTS_FAILED++))
fi

# Test 2: Stale answer rejection
log_info "=== Test 2: Stale answer rejection ==="
if "$W" send --dir "${SANDBOX_DIR}" --request-id stale-id --input "a" 2>&1 | grep -q "stale answer"; then
    log_info "✓ Stale answer rejection works"
    ((TESTS_PASSED++))
else
    log_error "✗ Stale answer rejection failed"
    ((TESTS_FAILED++))
fi

# Test 3: Valid answer
log_info "=== Test 3: Valid answer ==="
REQUEST_ID=$(rid)
if [ -n "$REQUEST_ID" ] && "$W" send --dir "${SANDBOX_DIR}" --request-id "$REQUEST_ID" --input "a" | grep -q "recorded answer"; then
    log_info "✓ Valid answer works"
    ((TESTS_PASSED++))
else
    log_error "✗ Valid answer failed"
    ((TESTS_FAILED++))
fi

# Test 4: Reconciliation
log_info "=== Test 4: Reconciliation ==="
if "$W" run --dir "${SANDBOX_DIR}" | grep -q "attempt 2 started"; then
    log_info "✓ Reconciliation works"
    ((TESTS_PASSED++))
else
    log_error "✗ Reconciliation failed"
    ((TESTS_FAILED++))
fi

# Test 5: Second question
log_info "=== Test 5: Second question handling ==="
REQUEST_ID=$(rid)
if [ -n "$REQUEST_ID" ] && "$W" send --dir "${SANDBOX_DIR}" --request-id "$REQUEST_ID" --input "yes" | grep -q "recorded answer"; then
    log_info "✓ Second question handling works"
    ((TESTS_PASSED++))
else
    log_error "✗ Second question handling failed"
    ((TESTS_FAILED++))
fi

# Test 6: Final completion
log_info "=== Test 6: Final completion ==="
if "$W" run --dir "${SANDBOX_DIR}" | grep -q "Succeeded"; then
    log_info "✓ Final completion works"
    ((TESTS_PASSED++))
else
    log_error "✗ Final completion failed"
    ((TESTS_FAILED++))
fi

# Failure mode tests
log_info "=== Testing failure modes ==="
mkdir -p /tmp/weave-agents-test
export SDK="$PWD/sdk/python"

cat > /tmp/weave-agents-test/nooutput.py <<'EOF'
print("forgot to publish outputs")
EOF

cat > /tmp/weave-agents-test/crash.py <<'EOF'
import os; os.kill(os.getpid(), 11)
EOF

cat > /tmp/weave-agents-test/huge.py <<'EOF'
import sys, os; sys.path.insert(0, os.environ["SDK"]); import weave
weave.output(report="x" * 5000)
EOF

cat > /tmp/weave-agents-test/transient.py <<'EOF'
import sys, os; sys.path.insert(0, os.environ["SDK"]); import weave
weave.fail("Transient", "upstream 503, safe to retry")
EOF

for mode in nooutput crash huge transient; do
    rm -rf "/tmp/weave-e-${mode}-${TIMESTAMP}"
    log_info "Testing ${mode} failure mode..."

    result=$("$W" run --dir "/tmp/weave-e-${mode}-${TIMESTAMP}" --output score:required \
      -- python3 "/tmp/weave-agents-test/${mode}.py" 2>/dev/null | grep -oE 'failed: .*' || echo "")

    if [ -n "$result" ]; then
        log_info "✓ ${mode} → ${result}"
        ((TESTS_PASSED++))
    else
        log_error "✗ ${mode} failed to produce expected failure"
        ((TESTS_FAILED++))
    fi
done

# Unit tests
log_info "=== Running unit tests ==="
if go test -v ./... 2>&1 | grep -q "ok"; then
    log_info "✓ Unit tests passed"
    ((TESTS_PASSED++))
else
    log_error "✗ Unit tests failed"
    ((TESTS_FAILED++))
fi

# Summary
echo ""
log_info "=== Test Summary ==="
log_info "Passed: ${TESTS_PASSED}"
log_error "Failed: ${TESTS_FAILED}"
echo ""

if [ $TESTS_FAILED -eq 0 ]; then
    log_info "All tests passed! 🎉"
    exit 0
else
    log_error "Some tests failed"
    exit 1
fi