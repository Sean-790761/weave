"""Unit tests for the weave agent SDK (sdk/python/weave.py).

No subprocess, no weave binary: these exercise the SDK's file/exit behaviour
directly. The module-global ``weave._asked`` counter is reset before each test.
"""
import json
import os

import pytest

import weave


@pytest.fixture(autouse=True)
def reset_ask_counter():
    weave._asked = 0
    yield


def test_output_stringifies_scalars_and_keeps_containers(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))
    weave.output(score=8.5, severity="major", findings=["a", "b"], meta={"k": 1})

    data = json.loads((tmp_path / "output.json").read_text())
    assert data["score"] == "8.5"          # scalar -> stringified
    assert data["severity"] == "major"
    assert data["findings"] == ["a", "b"]  # list -> preserved as JSON
    assert data["meta"] == {"k": 1}        # dict -> preserved as JSON


def test_ask_writes_askjson_and_exits77(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))
    monkeypatch.setenv("WEAVE_REQUEST_ID", "q1")

    with pytest.raises(SystemExit) as exc:
        weave.ask("severity? a/b/c")
    assert exc.value.code == 77

    ask = json.loads((tmp_path / ".weave" / "ask.json").read_text())
    assert ask["prompt"] == "severity? a/b/c"
    assert ask["requestId"] == "q1"


def test_ask_replay_returns_recorded_answers_in_order(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))
    answers = tmp_path / ".weave" / "answers.json"
    answers.parent.mkdir()
    answers.write_text(json.dumps(["a", "b"]))

    assert weave.ask("first?") == "a"
    assert weave.ask("second?") == "b"
    # No ask.json written on replay.
    assert not (tmp_path / ".weave" / "ask.json").exists()


def test_ask_consumes_resume_input(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))
    monkeypatch.setenv("WEAVE_RESUME_INPUT", "a")

    assert weave.ask("severity?") == "a"

    recorded = json.loads((tmp_path / ".weave" / "answers.json").read_text())
    assert recorded == ["a"]
    # The resume env was consumed by the ask().
    assert "WEAVE_RESUME_INPUT" not in os.environ


def test_state_roundtrip(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))
    assert weave.load_state() == {}

    weave.save_state({"findings": ["x", "y"], "n": 2})
    assert weave.load_state() == {"findings": ["x", "y"], "n": 2}


def test_fail_writes_failjson_and_exits1(tmp_path, monkeypatch):
    monkeypatch.setenv("WEAVE_WORKSPACE", str(tmp_path))

    with pytest.raises(SystemExit) as exc:
        weave.fail("Transient", "disk full")
    assert exc.value.code == 1

    fail = json.loads((tmp_path / ".weave" / "fail.json").read_text())
    assert fail == {"reason": "Transient", "message": "disk full"}
