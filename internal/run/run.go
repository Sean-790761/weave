// Package run drives a whole workflow: it is the wiring between the DAG
// scheduler (pkg/core) and the per-task machinery (internal/engine).
//
// The split is deliberate. Core decides *what* should happen and is a pure
// function; this package performs it and owns every side effect — creating
// task directories, invoking the engine, persisting state. Nothing here makes
// a scheduling decision, so a change of policy never means touching IO code,
// and no IO bug can be mistaken for a scheduling bug.
//
// Layout on disk:
//
//	<run>/topology.json             the submitted workflow, written once
//	<run>/tasks/<agent>/task.json   one task, in model.Store's own format
//	<run>/tasks/<agent>/workspace/  that task's PVC stand-in
//
// One workspace per task rather than one per run: two agents sharing a
// workspace would have the shim's "clear output.json before each attempt"
// step wipe the other agent's result. That is C5 (per-Task PVC granularity)
// expressed as a directory layout.
//
// The run has no status file. Its phase is derived from its tasks every time
// it is asked, so there is no second copy of the truth to drift after a
// crash — the same reason model.Task derives everything from disk (I6).
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Sean-790761/weave/internal/engine"
	"github.com/Sean-790761/weave/internal/executor"
	"github.com/Sean-790761/weave/internal/model"
	"github.com/Sean-790761/weave/pkg/core"
)

const (
	topologyFile = "topology.json"
	tasksDir     = "tasks"
)

// Driver owns one run directory. It holds no scheduling state of its own:
// every method re-reads the world, so a Driver created fresh after a crash
// behaves exactly like the one that died.
type Driver struct {
	Dir  string
	Exec executor.Executor
	Log  func(format string, args ...any)
}

func (d *Driver) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

func (d *Driver) topologyPath() string { return filepath.Join(d.Dir, topologyFile) }

func (d *Driver) taskStore(agent string) model.Store {
	return model.Store{Dir: filepath.Join(d.Dir, tasksDir, agent)}
}

// Submit records the topology. It refuses to overwrite one: a run's workflow
// is immutable, or the tasks already on disk stop being explainable by it.
func (d *Driver) Submit(topo *core.Topology) error {
	if err := topo.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(d.topologyPath()); err == nil {
		return fmt.Errorf("run %s already exists", d.Dir)
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(topo, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.topologyPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.topologyPath())
}

// Topology reads back what was submitted. It is re-validated on load: a file
// edited by hand between two steps must not be able to start an agent the
// submitted workflow never allowed.
func (d *Driver) Topology() (*core.Topology, error) {
	b, err := os.ReadFile(d.topologyPath())
	if err != nil {
		return nil, fmt.Errorf("no run in %s: %w", d.Dir, err)
	}
	return core.ParseJSON(b)
}

// Observe builds Core's view of the world by reading every task file. An
// agent with no task directory is simply absent — that absence is the only
// "not started yet" signal, on disk as well as in Core.
func (d *Driver) Observe() (core.Observed, error) {
	topo, err := d.Topology()
	if err != nil {
		return nil, err
	}
	obs := make(core.Observed, len(topo.Agents))
	for _, a := range topo.Agents {
		st := d.taskStore(a.Name)
		if !st.Exists() {
			continue
		}
		t, err := st.Load()
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", a.Name, err)
		}
		v := core.TaskView{
			Agent:          a.Name,
			Phase:          core.Phase(t.Status.Phase),
			FailureReason:  t.Status.FailureReason,
			FailureMessage: t.Status.FailureMessage,
		}
		// Outputs are only readable if they belong to the attempt the task is
		// on. Residue from a previous attempt must never reach a downstream
		// agent as if it were this attempt's result.
		if t.Status.OutputsAttempt == t.Spec.Attempt {
			v.Outputs = t.Status.Outputs
		}
		obs[a.Name] = v
	}
	return obs, nil
}

// Step advances the run by as much as it can without blocking, and reports
// whether anything moved. Callers loop until it reports false — at which
// point the run is finished or is waiting for a human.
func (d *Driver) Step(ctx context.Context) (bool, error) {
	topo, err := d.Topology()
	if err != nil {
		return false, err
	}
	obs, err := d.Observe()
	if err != nil {
		return false, err
	}
	actions, err := core.Decide(topo, obs)
	if err != nil {
		return false, err
	}

	progressed := false
	for _, act := range actions {
		if act.Kind != core.ActionStartTask {
			continue // RunSucceeded / RunFailed are for the caller, not for us
		}
		if err := d.createTask(topo.Name, act); err != nil {
			return false, fmt.Errorf("start %s: %w", act.Agent, err)
		}
		progressed = true
	}

	// Drive every task that exists, new or not. A task that is terminal, or
	// waiting on an unanswered question, costs one no-op reconcile; a task
	// left Running by a crash is picked back up here, which is what makes the
	// run as restartable as a single task is.
	for _, a := range topo.Agents {
		st := d.taskStore(a.Name)
		if !st.Exists() {
			continue
		}
		t, err := st.Load()
		if err != nil {
			return false, fmt.Errorf("task %s: %w", a.Name, err)
		}
		before := t.ResourceVersion
		eng := &engine.Engine{
			Exec:  d.Exec,
			Store: st,
			Log:   d.taskLogger(a.Name),
		}
		if err := eng.Run(ctx, t); err != nil {
			return false, fmt.Errorf("task %s: %w", a.Name, err)
		}
		if t.ResourceVersion != before {
			progressed = true
		}
	}
	return progressed, nil
}

// Run steps until the run stops moving: everything finished, one agent
// failed, or someone has to answer a question.
func (d *Driver) Run(ctx context.Context) error {
	for {
		progressed, err := d.Step(ctx)
		if err != nil {
			return err
		}
		if !progressed {
			return nil
		}
	}
}

// Phase is the run's phase, derived from its tasks.
func (d *Driver) Phase() (core.Phase, error) {
	topo, err := d.Topology()
	if err != nil {
		return "", err
	}
	obs, err := d.Observe()
	if err != nil {
		return "", err
	}
	return core.RunPhase(topo, obs), nil
}

// Question is one unanswered prompt, with the agent that asked it. The agent
// name is part of the address: in a DAG, "the run is waiting" is not enough
// to know what is being answered.
type Question struct {
	Agent     string
	Prompt    string
	RequestID string
}

// Questions lists what the run currently needs from a human, in topology
// order. More than one branch can be parked at once.
func (d *Driver) Questions() ([]Question, error) {
	topo, err := d.Topology()
	if err != nil {
		return nil, err
	}
	var out []Question
	for _, a := range topo.Agents {
		st := d.taskStore(a.Name)
		if !st.Exists() {
			continue
		}
		t, err := st.Load()
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", a.Name, err)
		}
		ui := t.Status.UserInput
		if t.Status.Phase != model.PhaseWaitingForUserInput || ui == nil || ui.Response != nil {
			continue
		}
		out = append(out, Question{Agent: a.Name, Prompt: ui.Prompt, RequestID: ui.RequestID})
	}
	return out, nil
}

// Answer records a human response against one agent's task. The requestId
// guard lives in engine.Answer; this only has to route to the right task,
// which is the part a DAG adds — answering "the run" would be ambiguous the
// moment two branches are both parked.
func (d *Driver) Answer(agent, requestID, response string, now time.Time) error {
	st := d.taskStore(agent)
	if !st.Exists() {
		return fmt.Errorf("agent %q has no task in this run", agent)
	}
	t, err := st.Load()
	if err != nil {
		return err
	}
	if err := engine.Answer(t, requestID, response, now); err != nil {
		return fmt.Errorf("agent %q: %w", agent, err)
	}
	return st.Save(t)
}

// Outputs returns what each finished agent published, for rendering a final
// result or debugging a run.
func (d *Driver) Outputs() (map[string]map[string]string, error) {
	obs, err := d.Observe()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(obs))
	for name, v := range obs {
		if v.Phase == core.PhaseSucceeded {
			out[name] = v.Outputs
		}
	}
	return out, nil
}

func (d *Driver) createTask(runName string, act core.Action) error {
	st := d.taskStore(act.Agent)
	if st.Exists() {
		// Decide only proposes agents with no task, so this means two Steps
		// raced. Creating a second task would silently duplicate an agent.
		return fmt.Errorf("task already exists")
	}
	t := &model.Task{
		ID: runName + "-" + act.Agent,
		Spec: model.TaskSpec{
			RunRef:    runName,
			AgentName: act.Agent,
			Attempt:   1,
			Image:     act.Image,
			Command:   act.Command,
			Env:       act.Env,
			Outputs:   toModelOutputs(act.Outputs),
		},
		Status: model.TaskStatus{Phase: model.PhasePending},
	}
	d.logf("start %s: %v", act.Agent, act.Command)
	return st.Save(t)
}

func (d *Driver) taskLogger(agent string) func(string, ...any) {
	if d.Log == nil {
		return nil
	}
	return func(format string, args ...any) {
		d.Log(agent+": "+format, args...)
	}
}

func toModelOutputs(decls []core.OutputDecl) []model.OutputDecl {
	if len(decls) == 0 {
		return nil
	}
	out := make([]model.OutputDecl, 0, len(decls))
	for _, d := range decls {
		out = append(out, model.OutputDecl{Name: d.Name, Required: d.Required})
	}
	return out
}
