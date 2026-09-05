package core

import (
	"reflect"
	"strings"
	"testing"
)

func done(agent string, outputs map[string]string) TaskView {
	return TaskView{Agent: agent, Phase: PhaseSucceeded, Outputs: outputs}
}

func TestRenderSubstitutes(t *testing.T) {
	obs := Observed{
		"planner":    done("planner", map[string]string{"plan": "step-1", "n": "3"}),
		"reviewer-a": done("reviewer-a", map[string]string{"score": "8.5"}),
	}
	cases := []struct{ in, want string }{
		{"{{ planner.output.plan }}", "step-1"},
		// no spaces inside the braces
		{"{{planner.output.plan}}", "step-1"},
		// two references in one string
		{"plan={{ planner.output.plan }} n={{ planner.output.n }}", "plan=step-1 n=3"},
		// dashed agent name
		{"{{ reviewer-a.output.score }}", "8.5"},
		// no reference at all
		{"nothing to do here", "nothing to do here"},
		{"", ""},
	}
	for _, c := range cases {
		got, err := Render(c.in, obs)
		if err != nil {
			t.Fatalf("Render(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Render(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderRefusesToInventAValue(t *testing.T) {
	obs := Observed{
		"planner": done("planner", map[string]string{"plan": "step-1"}),
		"slow":    {Agent: "slow", Phase: PhaseRunning},
	}
	cases := []struct{ name, in, want string }{
		{"unknown agent", "{{ ghost.output.x }}", "has not run"},
		{"agent still running", "{{ slow.output.x }}", "outputs are not readable yet"},
		{"missing key", "{{ planner.output.nope }}", `produced no output "nope"`},
		{"malformed", "{{ planner.outpt.plan }}", "malformed reference"},
		{"not a reference at all", "{{ }}", "malformed reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Render(c.in, obs)
			if err == nil {
				t.Fatalf("expected an error, got %q — an empty substitution reaches the agent as a hole", got)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// Substitution happens inside one argv element, so a value with spaces stays
// one argument. If this ever re-splits, an agent receiving a sentence as its
// output silently turns into an agent receiving five flags.
func TestRenderAllKeepsArgvBoundaries(t *testing.T) {
	obs := Observed{"planner": done("planner", map[string]string{"summary": "looks fine to me"})}
	got, err := RenderAll([]string{"review.py", "--note", "{{ planner.output.summary }}"}, obs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"review.py", "--note", "looks fine to me"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderEnv(t *testing.T) {
	obs := Observed{"planner": done("planner", map[string]string{"plan": "p1"})}
	got, err := RenderEnv(map[string]string{"PLAN": "{{ planner.output.plan }}", "MODE": "strict"}, obs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"PLAN": "p1", "MODE": "strict"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := RenderEnv(map[string]string{"X": "{{ ghost.output.x }}"}, obs); err == nil {
		t.Fatal("expected env error")
	} else if !strings.Contains(err.Error(), "env X") {
		t.Fatalf("error should name the key: %v", err)
	}
}
