"""Weave agent SDK.

Two functions matter:

    answer = weave.ask("choose a/b/c")   # pauses the task, resumes with the answer
    weave.output(score=8.5, summary="…") # publishes to downstream agents

Neither exit code 77 nor the termination log nor request IDs appear in agent
code. The shim handles all of it.

The SDK is optional. An agent that writes /workspace/output.json and exits 0
is a valid Weave agent.

How ask() resumes
-----------------
The container is torn down while a human thinks, so the process restarts from
main() and replays. Answers already given are recorded in .weave/answers.json
and returned in order, so the Nth ask() on a replay returns the Nth answer
without pausing again.

Replay assumes the code before an ask() is safe to run twice. That holds for
computation; it does not hold for anything with side effects. Use
save_state/load_state to skip work you must not repeat:

    st = weave.load_state()
    if "analysis" not in st:
        st["analysis"] = expensive_and_irreversible()
        weave.save_state(st)

This replay burden is the single biggest cost of the exit-77 design, and the
main reason a runtime with real pause/resume would be worth adopting.
"""

import json
import os
import sys

__all__ = ["ask", "output", "fail", "load_state", "save_state", "workspace"]

EXIT_ASK = 77
_asked = 0


def workspace() -> str:
    return os.getenv("WEAVE_WORKSPACE", "/workspace")


def _p(*parts) -> str:
    return os.path.join(workspace(), *parts)


def _read(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def _write(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(obj, f, ensure_ascii=False)
    os.replace(tmp, path)


def ask(prompt: str) -> str:
    """Ask a human. Returns their answer, possibly after a container restart."""
    global _asked
    answers = _read(_p(".weave", "answers.json"), [])

    if _asked < len(answers):          # replaying a question already answered
        a = answers[_asked]
        _asked += 1
        return a

    resume = os.environ.pop("WEAVE_RESUME_INPUT", None)
    if resume is not None:             # this is the question that paused us
        answers.append(resume)
        _write(_p(".weave", "answers.json"), answers)
        _asked += 1
        return resume

    _write(_p(".weave", "ask.json"), {
        "prompt": prompt,
        "requestId": os.getenv("WEAVE_REQUEST_ID", ""),
    })
    sys.stdout.flush()
    sys.exit(EXIT_ASK)


def output(**kwargs) -> None:
    """Publish outputs. Scalars are stringified; dicts/lists keep JSON form."""
    payload = {}
    for k, v in kwargs.items():
        payload[k] = v if isinstance(v, (dict, list)) else str(v)
    _write(_p("output.json"), payload)


def fail(reason: str = "Error", message: str = "") -> None:
    """Classify this failure so the scheduler can decide whether to retry."""
    _write(_p(".weave", "fail.json"), {"reason": reason, "message": message})
    sys.exit(1)


def load_state() -> dict:
    return _read(_p(".weave", "state.json"), {})


def save_state(state: dict) -> None:
    _write(_p(".weave", "state.json"), state)
