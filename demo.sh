#!/usr/bin/env bash
# End-to-end walk of the exit-77 loop. Every step is a separate process
# invocation on purpose: nothing is carried in memory between transitions,
# which is what makes this a test of I6 rather than a happy-path script.
set -euo pipefail

cd "$(dirname "$0")"
export HOME="${HOME:-/tmp}"
go build -o bin/weave ./cmd/weave

W="$PWD/bin/weave"
AGENT="$PWD/examples/reviewer.py"
DIR="${1:-/tmp/weave-demo}"
rm -rf "$DIR"

rid() { "$W" status --dir "$DIR" --json | python3 -c \
  'import json,sys;print(json.load(sys.stdin)["status"]["userInput"]["requestId"])'; }

hr() { printf '\n\033[1m── %s\033[0m\n' "$1"; }

hr "1. submit — runs until the agent asks for input"
"$W" run --dir "$DIR" --agent reviewer \
  --output score:required --output summary:required \
  -- python3 "$AGENT"

hr "2. a stale answer is refused (this is the double-send race)"
"$W" send --dir "$DIR" --request-id stale-id --input "a" || true

hr "3. answer the real question"
"$W" send --dir "$DIR" --request-id "$(rid)" --input "a"

hr "4. reconcile — new process, state comes only from disk"
"$W" run --dir "$DIR"

hr "5. answer the SECOND question (different requestId)"
"$W" send --dir "$DIR" --request-id "$(rid)" --input "yes"

hr "6. reconcile to completion"
"$W" run --dir "$DIR"

hr "failure modes"
mkdir -p /tmp/weave-agents
export SDK="$PWD/sdk/python"
cat > /tmp/weave-agents/nooutput.py <<'EOF'
print("forgot to publish outputs")
EOF
cat > /tmp/weave-agents/crash.py <<'EOF'
import os; os.kill(os.getpid(), 11)
EOF
cat > /tmp/weave-agents/huge.py <<'EOF'
import sys, os; sys.path.insert(0, os.environ["SDK"]); import weave
weave.output(report="x" * 5000)
EOF
cat > /tmp/weave-agents/transient.py <<'EOF'
import sys, os; sys.path.insert(0, os.environ["SDK"]); import weave
weave.fail("Transient", "upstream 503, safe to retry")
EOF

for c in nooutput crash huge transient; do
  rm -rf "/tmp/weave-e-$c"
  printf '\n  %-10s → ' "$c"
  "$W" run --dir "/tmp/weave-e-$c" --output score:required \
    -- python3 "/tmp/weave-agents/$c.py" 2>/dev/null \
    | grep -oE 'failed: .*' || true
done

hr "unit tests"
go test ./...
