package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Sean-790761/weave/internal/executor"
	"github.com/Sean-790761/weave/internal/model"
	"github.com/Sean-790761/weave/pkg/core"
)

// These are integration tests: a real weave binary, real shim, real child
// processes, real files. The unit tests in pkg/core pin the scheduling rules;
// what is being checked here is that the wiring between them is not a lie —
// that an output really does reach the next agent's argv, that a question
// really does park one branch and not the others, and that a run really can
// be resumed by a process that was not there when it started.
var weaveBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "weave-run-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	weaveBin = filepath.Join(dir, "weave")
	build := exec.Command("go", "build", "-o", weaveBin, "./cmd/weave")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
}

func writeAgent(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// submit starts a run in its own directory and returns a driver for it.
func submit(t *testing.T, topoJSON string) (*Driver, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	topo, err := core.ParseJSON([]byte(topoJSON))
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	d := driverFor(t, dir)
	if err := d.Submit(topo); err != nil {
		t.Fatalf("submit: %v", err)
	}
	return d, dir
}

// driverFor deliberately builds a *new* Driver every time it is called, so a
// test can hand each step to a driver that shares nothing with the last one.
func driverFor(t *testing.T, dir string) *Driver {
	return &Driver{
		Dir:  dir,
		Exec: &executor.Local{Bin: weaveBin},
		Log:  func(f string, a ...any) { t.Logf("[run] "+f, a...) },
	}
}

func mustRun(t *testing.T, d *Driver) {
	t.Helper()
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func mustPhase(t *testing.T, d *Driver, want core.Phase) {
	t.Helper()
	got, err := d.Phase()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		outs, _ := d.Outputs()
		obs, _ := d.Observe()
		t.Fatalf("run phase = %s, want %s\nobserved: %+v\noutputs: %v", got, want, obs, outs)
	}
}

func mustOutputs(t *testing.T, d *Driver) map[string]map[string]string {
	t.Helper()
	out, err := d.Outputs()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- agents -----------------------------------------------------------------

const plannerPy = `#!/usr/bin/env python3
import json, os
ws = os.environ["WEAVE_WORKSPACE"]
with open(os.path.join(ws, "output.json"), "w") as f:
    json.dump({"plan": "check the null deref"}, f)
print("planner done")
`

// Echoes back both channels a rendered value can arrive through: argv and env.
const reviewerPy = `#!/usr/bin/env python3
import json, os, sys
ws = os.environ["WEAVE_WORKSPACE"]
with open(os.path.join(ws, "output.json"), "w") as f:
    json.dump({"score": "8.5", "from_argv": sys.argv[1], "from_env": os.environ.get("NOTE", "<unset>")}, f)
print("reviewer done")
`

// Declares an output it never produces: the OutputMissing path.
const forgetfulPy = `#!/usr/bin/env python3
print("I did nothing")
`

func askerPy(t *testing.T) string {
	return fmt.Sprintf(`#!/usr/bin/env python3
import sys
sys.path.insert(0, %q)
import weave
sev = weave.ask("severity? a=blocker b=major c=minor")
print("severity =", sev)
weave.output(severity=sev)
`, filepath.Join(repoRoot(), "sdk", "python"))
}

func failingPy(t *testing.T) string {
	return fmt.Sprintf(`#!/usr/bin/env python3
import sys
sys.path.insert(0, %q)
import weave
weave.fail("Transient", "upstream service is flaky")
`, filepath.Join(repoRoot(), "sdk", "python"))
}

// --- tests ------------------------------------------------------------------

// The whole point of the DAG: one agent's declared output turns up in the
// next agent's arguments, without either agent knowing the other exists.
func TestOutputReachesTheNextAgent(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	planner := writeAgent(t, agents, "planner.py", plannerPy)
	reviewer := writeAgent(t, agents, "reviewer.py", reviewerPy)

	d, dir := submit(t, fmt.Sprintf(`{"name":"chain","agents":[
	  {"name":"planner","command":["python3",%q],
	   "outputs":[{"name":"plan","required":true}]},
	  {"name":"reviewer","dependsOn":["planner"],
	   "command":["python3",%q,"{{ planner.output.plan }}"],
	   "env":{"NOTE":"plan={{ planner.output.plan }}"},
	   "outputs":[{"name":"score","required":true}]}]}`, planner, reviewer))

	mustRun(t, d)
	mustPhase(t, d, core.PhaseSucceeded)

	outs := mustOutputs(t, d)
	if got := outs["reviewer"]["from_argv"]; got != "check the null deref" {
		t.Fatalf("argv substitution = %q", got)
	}
	if got := outs["reviewer"]["from_env"]; got != "plan=check the null deref" {
		t.Fatalf("env substitution = %q", got)
	}

	// Per-task workspaces (C5): the reviewer's attempt clears output.json in
	// its own workspace, and the planner's result must survive that.
	for _, agent := range []string{"planner", "reviewer"} {
		p := filepath.Join(dir, "tasks", agent, "workspace", "output.json")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s lost its workspace output: %v", agent, err)
		}
	}
}

// A human question parks one branch. Everything that does not depend on the
// answer must keep going, or one reviewer's coffee break stalls the workflow.
func TestQuestionParksOneBranchAndResumes(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	planner := writeAgent(t, agents, "planner.py", plannerPy)
	asker := writeAgent(t, agents, "asker.py", askerPy(t))
	sidekick := writeAgent(t, agents, "sidekick.py", plannerPy)
	merger := writeAgent(t, agents, "merger.py", reviewerPy)

	d, dir := submit(t, fmt.Sprintf(`{"name":"hitl","agents":[
	  {"name":"planner","command":["python3",%q],
	   "outputs":[{"name":"plan","required":true}]},
	  {"name":"asker","dependsOn":["planner"],"command":["python3",%q],
	   "outputs":[{"name":"severity","required":true}]},
	  {"name":"sidekick","dependsOn":["planner"],"command":["python3",%q],
	   "outputs":[{"name":"plan"}]},
	  {"name":"merger","dependsOn":["asker"],
	   "command":["python3",%q,"{{ asker.output.severity }}"],
	   "outputs":[{"name":"score","required":true}]}]}`,
		planner, asker, sidekick, merger))

	mustRun(t, d)
	mustPhase(t, d, core.PhaseWaitingForUserInput)

	qs, err := d.Questions()
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].Agent != "asker" {
		t.Fatalf("questions = %+v, want one from asker", qs)
	}
	if qs[0].Prompt != "severity? a=blocker b=major c=minor" || qs[0].RequestID == "" {
		t.Fatalf("question = %+v", qs[0])
	}

	// The independent branch finished while the other waits.
	obs, err := d.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if obs["sidekick"].Phase != core.PhaseSucceeded {
		t.Fatalf("sidekick should not wait on the human: %+v", obs["sidekick"])
	}
	// The blocked branch's downstream never started — no task, no workspace.
	if _, err := os.Stat(filepath.Join(dir, "tasks", "merger")); !os.IsNotExist(err) {
		t.Fatal("merger must not start before its dependency is answered")
	}

	// Routing and the stale-answer guard both apply through the run.
	now := time.Now().UTC()
	if err := d.Answer("asker", "not-the-request-id", "a", now); err == nil {
		t.Fatal("a stale requestId must be rejected through the run too")
	}
	if err := d.Answer("merger", "", "a", now); err == nil {
		t.Fatal("answering an agent with no task must fail")
	}

	// Answer with a driver that did not run the earlier steps: the question
	// and its requestId live on disk, not in this process.
	fresh := driverFor(t, dir)
	if err := fresh.Answer("asker", qs[0].RequestID, "a", now); err != nil {
		t.Fatalf("answer: %v", err)
	}
	mustRun(t, fresh)
	mustPhase(t, fresh, core.PhaseSucceeded)

	outs := mustOutputs(t, fresh)
	if got := outs["asker"]["severity"]; got != "a" {
		t.Fatalf("asker severity = %q", got)
	}
	if got := outs["merger"]["from_argv"]; got != "a" {
		t.Fatalf("the answer did not reach the downstream agent: %q", got)
	}
}

// I6 at the run level: hand every single step to a driver that has never seen
// the run before. If any scheduling state were held in memory, the run would
// stall or restart an agent here.
func TestRunSurvivesAControllerThatDiesEveryStep(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	planner := writeAgent(t, agents, "planner.py", plannerPy)
	reviewer := writeAgent(t, agents, "reviewer.py", reviewerPy)

	_, dir := submit(t, fmt.Sprintf(`{"name":"restart","agents":[
	  {"name":"planner","command":["python3",%q],
	   "outputs":[{"name":"plan","required":true}]},
	  {"name":"reviewer","dependsOn":["planner"],
	   "command":["python3",%q,"{{ planner.output.plan }}"],
	   "outputs":[{"name":"score","required":true}]}]}`, planner, reviewer))

	steps := 0
	for {
		if steps > 10 {
			t.Fatal("run did not settle within 10 steps")
		}
		progressed, err := driverFor(t, dir).Step(context.Background())
		if err != nil {
			t.Fatalf("step %d: %v", steps, err)
		}
		steps++
		if !progressed {
			break
		}
	}

	d := driverFor(t, dir)
	mustPhase(t, d, core.PhaseSucceeded)
	if got := mustOutputs(t, d)["reviewer"]["from_argv"]; got != "check the null deref" {
		t.Fatalf("from_argv = %q", got)
	}

	// A finished run is a fixed point: stepping it again must not re-run an
	// agent, which would mean a second attempt on a completed task.
	if progressed, err := d.Step(context.Background()); err != nil || progressed {
		t.Fatalf("finished run moved again: progressed=%v err=%v", progressed, err)
	}
	st := model.Store{Dir: filepath.Join(dir, "tasks", "reviewer")}
	task, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if task.Spec.Attempt != 1 {
		t.Fatalf("reviewer is on attempt %d; it was re-run", task.Spec.Attempt)
	}
}

// A failing agent ends the run and takes its reason with it; the downstream
// agent is never started with a hole where its input should be.
func TestFailureStopsTheRunAndKeepsTheReason(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	flaky := writeAgent(t, agents, "flaky.py", failingPy(t))
	reviewer := writeAgent(t, agents, "reviewer.py", reviewerPy)

	d, dir := submit(t, fmt.Sprintf(`{"name":"failing","agents":[
	  {"name":"flaky","command":["python3",%q],
	   "outputs":[{"name":"plan","required":true}]},
	  {"name":"reviewer","dependsOn":["flaky"],
	   "command":["python3",%q,"{{ flaky.output.plan }}"],
	   "outputs":[{"name":"score"}]}]}`, flaky, reviewer))

	mustRun(t, d)
	mustPhase(t, d, core.PhaseFailed)

	obs, err := d.Observe()
	if err != nil {
		t.Fatal(err)
	}
	// The agent classified its own failure; that classification is what a
	// retry policy will read, so it must survive the trip.
	if got := obs["flaky"].FailureReason; got != "Transient" {
		t.Fatalf("failure reason = %q, want Transient", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "reviewer")); !os.IsNotExist(err) {
		t.Fatal("downstream agent started despite the failure")
	}
}

// A declared-but-unproduced output fails the task rather than substituting an
// empty string downstream.
func TestDeclaredOutputThatNeverArrivesFailsTheRun(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	forgetful := writeAgent(t, agents, "forgetful.py", forgetfulPy)

	d, _ := submit(t, fmt.Sprintf(`{"name":"missing","agents":[
	  {"name":"forgetful","command":["python3",%q],
	   "outputs":[{"name":"score","required":true}]}]}`, forgetful))

	mustRun(t, d)
	mustPhase(t, d, core.PhaseFailed)

	obs, err := d.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if got := obs["forgetful"].FailureReason; got != "OutputMissing" {
		t.Fatalf("failure reason = %q, want OutputMissing", got)
	}
}

func TestSubmitRefusesToRewriteARun(t *testing.T) {
	d, _ := submit(t, `{"name":"once","agents":[{"name":"a","command":["true"]}]}`)
	topo, err := core.ParseJSON([]byte(`{"name":"once","agents":[{"name":"b","command":["true"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Submit(topo); err == nil {
		t.Fatal("a run's topology must be immutable once submitted")
	}
}

func TestTopologyIsRevalidatedOnLoad(t *testing.T) {
	d, dir := submit(t, `{"name":"tampered","agents":[{"name":"a","command":["true"]}]}`)
	// Someone edits the file between two steps.
	broken := `{"name":"tampered","agents":[{"name":"a","command":["true"],"dependsOn":["ghost"]}]}`
	if err := os.WriteFile(filepath.Join(dir, "topology.json"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Topology(); err == nil {
		t.Fatal("an edited topology must not be trusted")
	}
	if _, err := d.Step(context.Background()); err == nil {
		t.Fatal("Step must refuse to schedule from an invalid topology")
	}
}

// Core cannot import model, so the compiler cannot check that the two phase
// vocabularies agree. This test is that check: Observe casts one to the other
// directly, and a phase added on one side only would silently become a phase
// Core has never heard of.
func TestCoreAndModelAgreeOnPhases(t *testing.T) {
	pairs := []struct {
		c core.Phase
		m model.Phase
	}{
		{core.PhasePending, model.PhasePending},
		{core.PhaseRunning, model.PhaseRunning},
		{core.PhaseWaitingForUserInput, model.PhaseWaitingForUserInput},
		{core.PhaseSucceeded, model.PhaseSucceeded},
		{core.PhaseFailed, model.PhaseFailed},
	}
	for _, p := range pairs {
		if string(p.c) != string(p.m) {
			t.Fatalf("phase mismatch: core %q vs model %q", p.c, p.m)
		}
		if p.c.Terminal() != p.m.Terminal() {
			t.Fatalf("phase %q: Terminal() disagrees", p.c)
		}
	}
}

// The observed view must not hand a downstream agent outputs left over from
// an attempt the task has already moved past.
func TestOutputsFromAnOlderAttemptAreNotObserved(t *testing.T) {
	d, dir := submit(t, `{"name":"stale","agents":[{"name":"a","command":["true"],
	  "outputs":[{"name":"x"}]}]}`)
	st := model.Store{Dir: filepath.Join(dir, "tasks", "a")}
	if err := st.Save(&model.Task{
		ID:   "stale-a",
		Spec: model.TaskSpec{AgentName: "a", Attempt: 2},
		Status: model.TaskStatus{
			Phase:          model.PhaseSucceeded,
			Outputs:        map[string]string{"x": "from attempt 1"},
			OutputsAttempt: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	obs, err := d.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if got := obs["a"].Outputs; got != nil {
		t.Fatalf("stale outputs surfaced: %v", got)
	}
}

// A sanity check on the fixture format itself: task.json stays readable JSON,
// because it is the thing a person greps when a run misbehaves.
func TestTaskFileIsReadable(t *testing.T) {
	requirePython(t)
	agents := t.TempDir()
	planner := writeAgent(t, agents, "planner.py", plannerPy)
	d, dir := submit(t, fmt.Sprintf(`{"name":"readable","agents":[
	  {"name":"planner","command":["python3",%q],"outputs":[{"name":"plan"}]}]}`, planner))
	mustRun(t, d)

	b, err := os.ReadFile(filepath.Join(dir, "tasks", "planner", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("task.json is not readable JSON: %v", err)
	}
	if m["spec"] == nil || m["status"] == nil {
		t.Fatalf("task.json lost its shape: %s", b)
	}
}
