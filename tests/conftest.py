"""Shared pytest fixtures for weave.

Puts ``sdk/python`` on ``sys.path`` so unit tests can ``import weave``
directly, builds the weave binary once per session for the e2e tests, and
gives each e2e module an isolated sandbox via the :class:`WeaveCLI` helper.
"""
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
SDK_PYTHON = ROOT / "sdk" / "python"

# Make ``import weave`` resolve to sdk/python/weave.py in unit tests.
if str(SDK_PYTHON) not in sys.path:
    sys.path.insert(0, str(SDK_PYTHON))


class WeaveCLI:
    """Drives the weave binary against an isolated task directory.

    Every subprocess gets ``--dir <sandbox>`` and a PYTHONPATH pointing at the
    SDK, so the agent under test can ``import weave`` without it being
    installed. No global ``os.chdir`` / ``os.environ`` mutation.
    """

    def __init__(self, bin_path: str, sandbox: str):
        self.bin = bin_path
        self.dir = sandbox
        self.env = {**os.environ, "PYTHONPATH": str(SDK_PYTHON)}
        os.makedirs(self.dir, exist_ok=True)

    def _cmd(self, sub: str) -> list[str]:
        # weave dispatches on the subcommand (os.Args[1]), so it must come
        # before --dir: `weave run --dir D ...`, not `weave --dir D run ...`.
        return [self.bin, sub, "--dir", self.dir]

    def run(self, extra: list[str], cwd: str | None = None) -> subprocess.CompletedProcess:
        """`weave run <extra>`. extra includes --agent/--output/-- <cmd>."""
        return subprocess.run(
            self._cmd("run") + extra,
            capture_output=True, text=True, env=self.env, cwd=cwd,
        )

    def send(self, request_id: str, answer: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            self._cmd("send") + ["--request-id", request_id, "--input", answer],
            capture_output=True, text=True, env=self.env,
        )

    def status(self) -> dict:
        r = subprocess.run(
            self._cmd("status") + ["--json"],
            capture_output=True, text=True, env=self.env,
        )
        assert r.returncode == 0, f"weave status failed: {r.stderr}"
        import json
        return json.loads(r.stdout)


@pytest.fixture(scope="session")
def weave_bin():
    """Build the weave binary once; reuse across the whole test session."""
    r = subprocess.run(
        ["go", "build", "-o", str(ROOT / "bin" / "weave"), "./cmd/weave"],
        cwd=str(ROOT), capture_output=True, text=True,
    )
    assert r.returncode == 0, f"go build failed:\n{r.stderr}"
    return str(ROOT / "bin" / "weave")


@pytest.fixture(scope="module")
def cli(weave_bin):
    """An isolated task directory + CLI wrapper for one e2e module."""
    sandbox = tempfile.mkdtemp(prefix="weave-e2e-")
    yield WeaveCLI(weave_bin, sandbox)
    shutil.rmtree(sandbox, ignore_errors=True)
