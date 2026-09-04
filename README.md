# weave — W1–2 vertical slice

Gets the exit-77 loop running with the least machinery possible, to validate the v1.1 agent contract. **No DAG, no Core, no Kubernetes.**

This slice comes before the horizontal build order in design doc §9 because the exit-77 loop is the most uncertain part of the project and the most likely to invalidate the design. Following 1→2→3→4→5 would put the first end-to-end run in week 10.

## Run it

```bash
bash demo.sh
```

Needs Go 1.22+ and python3. Standard library only, no external dependencies.

## What it proves

**The full loop**: `ask → exit 77 → envelope lands in the termination log → workload released (workspace preserved) → send → attempt++ → resume injected → replay → Succeeded`

**I6 (state survives restart)**: every step of the demo is a separate process invocation. Nothing is carried in memory between transitions — every state change is derived from `task.json` on disk alone. That is the equivalent of the controller crashing and coming back.

**Idempotent interaction across multiple questions**: the example agent asks **two** questions, not one. That is exactly where a naive single-shot resume breaks — the second question must not be answered by the first answer. Step 2 of the demo shows a stale requestId being rejected:

```
weave: stale answer: you are answering deadbeef but the task is now asking 786740adb27d07ab
```

**Attributable failures**: four degenerate paths, four distinct reasons, no empty states.

```
nooutput   → OutputMissing      declared required, never wrote it
crash      → Error (exit 139)   SIGSEGV; the agent never got to write anything
huge       → OutputTooLarge     5057 > 4096, degraded rather than truncated
transient  → Transient          agent classified itself, for the retry policy to consume
```

The `crash` case is the main reason the shim exists: an agent killed by a signal cannot write anything itself, so only an outer wrapper can guarantee an envelope.

**A non-invasive contract**: `examples/reviewer.py` contains no exit 77, no termination log, no request IDs. And the SDK is optional — writing `output.json` and exiting 0 is a valid Weave agent; the shim covers the rest. This is what decides whether off-the-shelf agents can be dropped in.

## Layout

```
internal/envelope/   wire format + 4KiB budget
internal/shim/       entrypoint wrapper; guarantees an envelope on every exit path
internal/model/      Task state (mirrors WeaveTask.status) + atomic file store
internal/executor/   runtime-neutral interface + Capabilities/Pause/Resume + Local
internal/engine/     level-triggered reconcile
sdk/python/weave.py  ask() / output() / save_state()
```

`internal/engine` is **not** `pkg/core`. Core stays a pure function over `(Topology, ObservedTasks)`; this is the platform glue that will sit behind it. When Core gets written in W3–5, resist moving this state machine into it — the Cancel calls, timestamps and store access all live at this layer, and pulling them into Core breaks I6.

## Deliberately absent

| Missing | Where it goes |
|---|---|
| DAG, fan-out, sliding window, lineage | `pkg/core`, W3–5 |
| CRDs, real controller, Pods | `pkg/crd` + `pkg/controller`, W6–8 |
| Retry / timeout / WaitingForUserInput timeout | W9–10 |
| attach, ttyd sidecar | W11–12 |
| Notification (`weave wait`, webhooks) | W13–14 |

## Two known design debts

**The replay assumption.** `weave.ask()` recovers by replaying: after the container is rebuilt the process restarts from `main()`, and questions already answered are returned in order from `.weave/answers.json`. This requires the code before an `ask()` to be safe to run twice. That holds for computation; it does not hold for side effects. `save_state`/`load_state` is the escape hatch, demonstrated in the example.

This replay burden is the largest cost of the exit-77 design and the main reason a freezable runtime (agent-sandbox pause, CRIU, Firecracker snapshot) is worth evaluating. The interface already reserves for it via `Capabilities{Pause}`, but verify first whether that pause is a real freeze or just pod deletion with the PVC kept. One day of verification; if it is a real freeze, the entire replay contract can be deleted and two to three weeks come back.

**The Local executor is synchronous.** `Ensure` blocks until the agent exits, so `Observe` does almost nothing. A Pod executor will be asynchronous and `Observe` will carry the weight. Both satisfy the same interface because `Ensure` is specified as "idempotently guarantee this attempt has run" rather than "start" — that wording is worth keeping.
