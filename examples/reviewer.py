#!/usr/bin/env python3
"""A reviewer agent with two human checkpoints.

Two asks, not one, because that is where the naive single-shot resume design
breaks: the second question must not be answered by the first answer.
"""
import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sdk", "python"))
import weave

def main():
    state = weave.load_state()

    if "findings" not in state:
        print("analysing diff …")
        state["findings"] = ["missing null check", "unbounded goroutine"]
        weave.save_state(state)      # side effect guarded: never redone on replay
    else:
        print("analysis restored from state, skipping")

    print(f"found {len(state['findings'])} issues")

    severity = weave.ask("severity? a=blocker b=major c=minor")
    print(f"severity = {severity}")

    approve = weave.ask("approve the merge? yes/no")
    print(f"approve = {approve}")

    weave.output(
        score="8.5" if approve == "yes" else "3.0",
        severity=severity,
        summary=f"{len(state['findings'])} issues, approve={approve}",
        findings=state["findings"],      # list -> stored as JSON text
    )
    print("done")

if __name__ == "__main__":
    main()
