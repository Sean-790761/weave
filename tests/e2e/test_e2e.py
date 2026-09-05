"""End-to-end lifecycle for the reviewer agent.

These six tests are a single ordered protocol — each advances the same task
and the next assumes the prior state. pytest runs them in definition order,
which is what makes the sequence work; do not reorder or parallelise.

The flow:
  submit (exit 77) → reject stale answer → answer → resume (attempt 2, 2nd
  question) → answer → resume → Succeeded with score/summary outputs.
"""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REVIEWER = str(ROOT / "examples" / "reviewer.py")


def _status(cli) -> dict:
    return cli.status()


def test_01_initial_submit_pauses_on_first_ask(cli):
    r = cli.run([
        "--agent", "reviewer",
        "--output", "score:required",
        "--output", "summary:required",
        "--", "python3", REVIEWER,
    ])
    assert r.returncode == 0, r.stderr
    st = _status(cli)
    assert st["status"]["phase"] == "WaitingForUserInput"
    ui = st["status"]["userInput"]
    assert ui["prompt"] == "severity? a=blocker b=major c=minor"
    assert ui["requestId"], "expected a requestId on the first question"


def test_02_stale_answer_is_rejected(cli):
    r = cli.send("stale-id", "a")
    assert r.returncode != 0, "stale send should fail"
    assert "stale answer" in r.stderr, r.stderr
    # The question must still be unanswered.
    assert _status(cli)["status"]["userInput"]["response"] is None


def test_03_valid_answer_is_recorded(cli):
    request_id = _status(cli)["status"]["userInput"]["requestId"]
    r = cli.send(request_id, "a")
    assert r.returncode == 0, r.stderr
    assert "recorded answer" in r.stdout, r.stdout
    assert _status(cli)["status"]["userInput"]["response"] == "a"


def test_04_resume_reaches_second_question(cli):
    r = cli.run([])
    assert r.returncode == 0, r.stderr
    st = _status(cli)
    assert st["spec"]["attempt"] == 2, st
    assert st["status"]["phase"] == "WaitingForUserInput"
    ui = st["status"]["userInput"]
    assert ui["prompt"] == "approve the merge? yes/no"
    assert ui["requestId"], "expected a requestId on the second question"


def test_05_second_answer_is_recorded(cli):
    request_id = _status(cli)["status"]["userInput"]["requestId"]
    r = cli.send(request_id, "yes")
    assert r.returncode == 0, r.stderr
    assert _status(cli)["status"]["userInput"]["response"] == "yes"


def test_06_final_completion_succeeds_with_outputs(cli):
    r = cli.run([])
    assert r.returncode == 0, r.stderr
    st = _status(cli)
    assert st["status"]["phase"] == "Succeeded"
    outputs = st["status"]["outputs"]
    assert outputs.get("score"), outputs
    assert outputs.get("summary"), outputs
    assert outputs.get("severity") == "a", outputs
