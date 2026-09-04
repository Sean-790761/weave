# Weave Design v1.1 — delta against v1.0

Only what v1.0 was missing or got wrong. Unmentioned sections are unchanged.

Date: 2026-09-04 · Status: validated alongside the spike code

---

## Summary of changes

| # | Change | Problem in v1.0 |
|---|---|---|
| C1 | Added an **agent output contract** (Appendix A) | Only the input side was defined (exit 77 + resume env); `{{ planner.output }}` had no definition at all |
| C2 | Prompt and output travel via the **termination message**, not the log | "parse the prompt from the logs" — the logs are gone the moment the pod is deleted, which breaks I6 |
| C3 | Added an **entrypoint shim**; agents never see the protocol | §5.2 required the agent to hand-roll a state machine, so no off-the-shelf agent could be used |
| C4 | fan-in **defaults to gather; fan-out must be explicit** (Appendix B) | You cannot tell from the YAML how many pods `reviewer` will start |
| C5 | PVC granularity fixed at **per-Task** (Appendix C) | Drawn at Run level; with `maxConcurrency: 10` ten pods overwrite one checkpoint, and sharing requires RWX |
| C6 | `requestId` pushed down to `WeaveTask.status.userInput` | It only existed on the Run, so a retried `weave send` could answer the wrong question |
| C7 | Executor gains `Capabilities/Pause/Resume` | Reserves for freezable runtimes such as agent-sandbox, defaulting to the current delete-pod fallback |
| C8 | L2 capacity and artifact boundaries stated | Undeclared, so someone will test with 5000 items and draw the wrong conclusion |

**Loose ends fixed**: `AGENTFLOW_RESUME_INPUT` → `WEAVE_RESUME_INPUT` throughout; R1/R3 need definitions (only R2 has fields); `itemsSnapshot` added to `WeaveRun.status` (I7 declared it, no field existed).

---

## Appendix A: agent contract v1

### A.1 Transport

An agent's **log plane** and **data plane** are separate. stdout carries human-readable logs only and never structured data — it is noisy, it is lossy, and it is unreadable once the pod is deleted.

Structured data travels through the container termination message: the main container writes `/dev/termination-log`, the kubelet records it at `pod.status.containerStatuses[].state.terminated.message`, and it lands in etcd with the pod status. **Limit 4KiB.** This is the only thing that keeps the prompt readable after WaitingForUserInput deletes the pod.

### A.2 The envelope

Prompt, output and failure share one format, so the controller parses one thing:

```json
{ "v":1, "kind":"done",    "attempt":1, "outputs": {"score":"8.5"} }
{ "v":1, "kind":"waiting", "attempt":1, "prompt":"choose a/b/c", "requestId":"abc123" }
{ "v":1, "kind":"failed",  "attempt":1, "reason":"Transient", "message":"…" }
```

`reason` is one of `Transient` / `Error` / `Timeout` (matching the Core classification) plus `OutputTooLarge` / `OutputMissing` / `ContractViolation` (contract breaches).

`outputs` is `map[string]string`: scalars as raw strings, structured values as their compact JSON text. Deliberately not nested JSON — the CRD schema stays flat, the etcd diff stays small, and template rendering does no type conversion.

### A.3 The shim

The main container command becomes `["weave","shim","--", <original entrypoint>]`. The shim:

1. Passes stdin/stdout/stderr straight through, so `attach` and `logs` behave as before
2. Clears `output.json`, `.weave/ask.json` and `.weave/fail.json` before starting, isolating retries
3. Forwards SIGTERM/SIGINT to the child, giving the agent a chance to checkpoint on cancel or node drain
4. Assembles and writes the envelope on child exit, keyed on the exit code

It **guarantees an envelope on every exit path**, including SIGSEGV (139) and OOMKill, so a crashed agent never leaves an unattributable empty status.

An agent only needs to write `/workspace/output.json` and exit 0 — **no SDK required**. The SDK exists purely to hide exit 77, request IDs and replay:

```python
answer = weave.ask("choose a/b/c")     # pauses the task, resumes with the answer
weave.output(score=8.5, summary="…")   # publishes to downstream agents
```

### A.4 Topology declares outputs

```yaml
agents:
  - name: planner
    outputs:
      - name: score
        required: true
      - name: summary
```

The value is not type checking; it is giving `Validate()` something to check. Today a typo in `{{ planner.outpt.score }}` only surfaces after planner has run every item. With a declaration it is caught at submit time.

**Declared required but not produced → the Task fails with `OutputMissing`.** Never silently substitute an empty string: the downstream agent receives `"Review result: "` and confabulates around the hole.

### A.5 Lifecycle rules

- **Clear on retry**: `attempt++` wipes `status.outputs`; a mismatched `outputsAttempt` is never trusted
- **Freeze after Succeeded**: re-rendering a downstream prompt after a controller restart must be byte-identical (I6). If an `attach` session mutates state such that an output changes, that is the first concrete trigger for `DivergedAt`
- **Render once**: `{{ }}` appearing inside an agent's output is not re-evaluated. This is both a correctness rule and an injection boundary — upstream output flows directly into a downstream prompt, and that trust relationship belongs in the security section

---

## Appendix B: fan-in / fan-out semantics

When `planner` expands to 3 items and `reviewer` writes `{{ planner.output }}`, the YAML does not say whether that is 1 pod or 3. **Do not infer. Be explicit.**

```yaml
# gather: reviewer is one task, receiving all planner results
- name: reviewer
  dependencies: [planner]
  prompt: "Summarise: {{ gather(planner.outputs.summary) }}"     # → JSON array

# broadcast: reviewer expands alongside planner into 3 tasks
- name: reviewer
  forEach:
    inherit: planner      # reuses the upstream frozen itemsSnapshot
  prompt: "Review {{ item }}: {{ planner.outputs.summary }}"     # same item scope, scalar
```

Two hard rules:

1. An ungathered reference is **legal only within the same fan-out scope**. Cross-scope without gather → `Validate()` errors rather than guessing
2. `forEach.inherit` must reuse the upstream `itemsSnapshot` (I7), or the two expansions can disagree on the item set

Gather is the default rather than broadcast because silently starting N times the pods is the more expensive and less visible failure mode.

The partial-failure policy has to be decided now; it is hard to retrofit:

```yaml
gatherPolicy: all      # all (default) | succeeded | any
```

---

## Appendix C: PVC granularity

**Granularity is per-Task**, named `<run>-<agent>-<item>`, RWO, reused across attempts.

A Run-level shared PVC means ten pods writing one `.checkpoint.json` at `maxConcurrency: 10`, and sharing requires RWX — but the K3s default local-path provisioner only offers RWO. Single-node happens to work; multi-node fails scheduling outright, which contradicts the "zero external dependencies, just K3s" positioning.

Cost: PVC count explodes on wide fan-outs. Needs GC once the Run reaches a terminal state (`ttlAfterFinished`).

---

## Appendix D: interaction idempotency

`requestId` must live on `WeaveTask.status.userInput`, not only in the Run's aggregate view.

The race: the user runs `weave send --input "a"`, the network times out, the user retries — meanwhile the agent has resumed, reached the next decision point and raised a **different** prompt. The second send answers the wrong question.

Rules:

- `weave send` must carry `--request-id`, read from `weave status`
- A mismatch is rejected, with an error naming what is currently being asked
- An answered question cannot be overwritten

---

## Appendix E: L2 scope boundaries

**Capacity**: `WeaveTask` is a per-item CRD, so every status update is an etcd write plus a cluster-wide watch broadcast, and the K3s default sqlite backend tops out earlier. L2 declares support for **≤500 tasks/run**; beyond that, L3 and PostgreSQL.

**Artifacts vs parameters**:

> Weave passes **parameters** between agents (scalars, short strings, small structured objects), not **artifacts** (files, reports, weights). Write artifacts to shared storage and pass the path or URI as an output.

Escape hatch:

```yaml
outputs:
  - name: report
    type: artifact     # value must be a URI or path; Weave does not interpret it
```

Real artifact passing means either cross-task PVC mounting (RWO conflicts, cross-node scheduling, lifecycle reclamation) or object storage — and the latter directly violates the zero-dependency positioning. That is L3 work.

**Over-limit behaviour**: an envelope above 4KiB degrades to `failed(OutputTooLarge)` and is **not truncated**. Truncation produces syntactically invalid JSON, the downstream parse fails, and the error points somewhere unrelated to the cause.

---

## Appendix F: executor capability negotiation

```go
type Capabilities struct { Pause, Snapshot, WarmPool bool }

type Executor interface {
    Ensure(ctx, *ExecSpec) (*Handle, error)
    Observe(ctx, *Handle) (*State, error)
    Cancel(ctx, *Handle) error
    Pause(ctx, *Handle) error                        // ErrNotSupported if unavailable
    Resume(ctx, *Handle, env map[string]string) error
    Capabilities() Capabilities
}
```

`ExecSpec` must contain **no** PodSpec, PVC name or RuntimeClass. The moment one appears the abstraction is fake and the second implementation will not fit. Durable state is referenced through `Handle.Ref`, an opaque executor-defined string.

`kubernetes-sigs/agent-sandbox` is a **peer implementation** of Executor, not another case in the §7.1 `switch runtimeClass`. It is itself a sandbox orchestrator that manages pods via RuntimeClass, sitting one layer above gVisor and Kata.

L2 reserves the interface; L3 implements `SandboxExecutor`. One thing to verify during the spike: **whether its pause is a real freeze (memory snapshot) or just pod deletion with the PVC retained**. If the former, the replay burden in A.3 disappears entirely and agent-side changes drop to zero. If the latter, the only gain is maintaining less code.
