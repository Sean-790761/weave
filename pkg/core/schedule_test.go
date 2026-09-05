package core

import (
	"reflect"
	"testing"
)

func kinds(as []Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, string(a.Kind)+":"+a.Agent)
	}
	return out
}

func mustDecide(t *testing.T, topo *Topology, obs Observed) []Action {
	t.Helper()
	as, err := Decide(topo, obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return as
}

func TestDecideStartsRootsOnly(t *testing.T) {
	topo := mustParse(t, diamond)
	got := mustDecide(t, topo, Observed{})
	if want := []string{"StartTask:planner"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("got %v, want %v", kinds(got), want)
	}
	if !reflect.DeepEqual(got[0].Command, []string{"python3", "planner.py"}) {
		t.Fatalf("command = %v", got[0].Command)
	}
}

func TestDecideWaitsForDependencies(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{"planner": {Agent: "planner", Phase: PhaseRunning}}
	if got := mustDecide(t, topo, obs); len(got) != 0 {
		t.Fatalf("nothing should start while planner runs, got %v", kinds(got))
	}
}

// The reason the DAG exists: a downstream agent is started with its
// upstream's output already substituted in.
func TestDecideRendersUpstreamOutputIntoTheCommand(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{"planner": done("planner", map[string]string{"plan": "step-1"})}
	got := mustDecide(t, topo, obs)
	if want := []string{"StartTask:reviewer-a", "StartTask:reviewer-b"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("got %v, want %v (topology order)", kinds(got), want)
	}
	want := []string{"python3", "review.py", "step-1"}
	if !reflect.DeepEqual(got[0].Command, want) {
		t.Fatalf("reviewer-a command = %v, want %v", got[0].Command, want)
	}
	if len(got[0].Outputs) != 1 || got[0].Outputs[0].Name != "score" {
		t.Fatalf("declared outputs must travel with the task: %+v", got[0].Outputs)
	}
}

// Level-triggered: Decide is asked again after nothing has changed and must
// not propose to start the same agents a second time.
func TestDecideIsIdempotent(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{"planner": done("planner", map[string]string{"plan": "step-1"})}
	first := mustDecide(t, topo, obs)
	second := mustDecide(t, topo, obs)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Decide is not deterministic:\n%v\n%v", kinds(first), kinds(second))
	}
	// Once the platform has created those tasks, they stop being proposals.
	obs["reviewer-a"] = TaskView{Agent: "reviewer-a", Phase: PhasePending}
	obs["reviewer-b"] = TaskView{Agent: "reviewer-b", Phase: PhaseRunning}
	if got := mustDecide(t, topo, obs); len(got) != 0 {
		t.Fatalf("started tasks must not be restarted, got %v", kinds(got))
	}
}

// A human question parks one branch. The other branch must keep moving, or a
// workflow with any human in it serialises on that human.
func TestWaitingBlocksOnlyItsOwnBranch(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{
		"planner":    done("planner", map[string]string{"plan": "step-1"}),
		"reviewer-a": {Agent: "reviewer-a", Phase: PhaseWaitingForUserInput},
	}
	got := mustDecide(t, topo, obs)
	if want := []string{"StartTask:reviewer-b"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("got %v, want %v", kinds(got), want)
	}
	// ...and merger, which depends on the parked branch, stays put.
	obs["reviewer-b"] = done("reviewer-b", map[string]string{"score": "7"})
	if got := mustDecide(t, topo, obs); len(got) != 0 {
		t.Fatalf("merger must wait for reviewer-a, got %v", kinds(got))
	}
}

func TestDecideFailsFast(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{
		"planner": done("planner", map[string]string{"plan": "step-1"}),
		"reviewer-a": {
			Agent: "reviewer-a", Phase: PhaseFailed,
			FailureReason: "OutputMissing", FailureMessage: "declared score, produced nothing",
		},
	}
	got := mustDecide(t, topo, obs)
	if len(got) != 1 || got[0].Kind != ActionRunFailed {
		t.Fatalf("want exactly one RunFailed, got %v", kinds(got))
	}
	if got[0].Agent != "reviewer-a" || got[0].Reason != "OutputMissing" {
		t.Fatalf("the run must name the culprit and its reason: %+v", got[0])
	}
}

func TestDecideReportsSuccessOnlyWhenEveryAgentIsDone(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{
		"planner":    done("planner", map[string]string{"plan": "p"}),
		"reviewer-a": done("reviewer-a", map[string]string{"score": "8"}),
		"reviewer-b": done("reviewer-b", map[string]string{"score": "7"}),
	}
	got := mustDecide(t, topo, obs)
	if want := []string{"StartTask:merger"}; !reflect.DeepEqual(kinds(got), want) {
		t.Fatalf("got %v, want %v", kinds(got), want)
	}
	if got[0].Env["A"] != "8" || got[0].Env["B"] != "7" {
		t.Fatalf("fan-in values not rendered: %v", got[0].Env)
	}
	obs["merger"] = done("merger", nil)
	got = mustDecide(t, topo, obs)
	if len(got) != 1 || got[0].Kind != ActionRunSucceeded {
		t.Fatalf("want RunSucceeded, got %v", kinds(got))
	}
}

// Validate proved the references are resolvable in principle; Decide can
// still be handed state where they are not, and must refuse rather than
// start an agent with a hole in its argv.
func TestDecideRefusesToStartWithAnUnresolvableReference(t *testing.T) {
	topo := mustParse(t, diamond)
	obs := Observed{"planner": done("planner", map[string]string{"something-else": "x"})}
	if _, err := Decide(topo, obs); err == nil {
		t.Fatal("expected Decide to refuse")
	}
}

func TestRunPhaseIsDerivedFromTheTasks(t *testing.T) {
	topo := mustParse(t, diamond)
	ok := func(n string) TaskView { return done(n, map[string]string{"score": "1", "plan": "p"}) }

	cases := []struct {
		name string
		obs  Observed
		want Phase
	}{
		{"nothing started", Observed{}, PhasePending},
		{"one running", Observed{"planner": {Phase: PhaseRunning}}, PhaseRunning},
		{"one done, rest not started", Observed{"planner": ok("planner")}, PhaseRunning},
		{"parked on a question", Observed{"planner": ok("planner"), "reviewer-a": {Phase: PhaseWaitingForUserInput}}, PhaseWaitingForUserInput},
		{"waiting but another still running", Observed{
			"planner": ok("planner"), "reviewer-a": {Phase: PhaseWaitingForUserInput}, "reviewer-b": {Phase: PhaseRunning},
		}, PhaseRunning},
		{"any failure wins", Observed{"planner": ok("planner"), "reviewer-a": {Phase: PhaseFailed}}, PhaseFailed},
		{"all done", Observed{
			"planner": ok("planner"), "reviewer-a": ok("reviewer-a"), "reviewer-b": ok("reviewer-b"), "merger": ok("merger"),
		}, PhaseSucceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RunPhase(topo, c.obs); got != c.want {
				t.Fatalf("RunPhase = %s, want %s", got, c.want)
			}
		})
	}
}

// The image is not used by the local executor, but it is what a Pod-backed
// one starts; losing it here would only show up on a cluster.
func TestDecideCarriesTheImage(t *testing.T) {
	topo := mustParse(t, `{"name":"i","agents":[
		{"name":"a","image":"ghcr.io/acme/reviewer:1.2","command":["true"]}]}`)
	got := mustDecide(t, topo, Observed{})
	if len(got) != 1 || got[0].Image != "ghcr.io/acme/reviewer:1.2" {
		t.Fatalf("image not carried: %+v", got)
	}
}
