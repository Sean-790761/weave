package core

import (
	"reflect"
	"strings"
	"testing"
)

// A diamond: one planner, two independent reviewers, one merger that fans
// them back in. Small enough to read, big enough to catch ordering bugs.
const diamond = `{
  "name": "review",
  "agents": [
    {"name": "planner",
     "command": ["python3", "planner.py"],
     "outputs": [{"name": "plan", "required": true}]},

    {"name": "reviewer-a",
     "dependsOn": ["planner"],
     "command": ["python3", "review.py", "{{ planner.output.plan }}"],
     "outputs": [{"name": "score"}]},

    {"name": "reviewer-b",
     "dependsOn": ["planner"],
     "command": ["python3", "review.py", "--strict"],
     "outputs": [{"name": "score"}]},

    {"name": "merger",
     "dependsOn": ["reviewer-a", "reviewer-b"],
     "command": ["python3", "merge.py"],
     "env": {"A": "{{ reviewer-a.output.score }}", "B": "{{ reviewer-b.output.score }}"}}
  ]
}`

func mustParse(t *testing.T, src string) *Topology {
	t.Helper()
	topo, err := ParseJSON([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return topo
}

// agents wraps agent bodies in a minimal valid topology, so each rejection
// case below is one line of the thing under test.
func agents(body string) string { return `{"name":"t","agents":[` + body + `]}` }

func TestParseReadsTheShape(t *testing.T) {
	topo := mustParse(t, diamond)
	if topo.Name != "review" {
		t.Fatalf("name = %q", topo.Name)
	}
	if len(topo.Agents) != 4 {
		t.Fatalf("got %d agents", len(topo.Agents))
	}
	planner, ok := topo.Agent("planner")
	if !ok {
		t.Fatal("planner missing")
	}
	if !reflect.DeepEqual(planner.Command, []string{"python3", "planner.py"}) {
		t.Fatalf("command = %v", planner.Command)
	}
	if len(planner.Outputs) != 1 || !planner.Outputs[0].Required {
		t.Fatalf("outputs = %+v", planner.Outputs)
	}
	merger, _ := topo.Agent("merger")
	if merger.Env["A"] != "{{ reviewer-a.output.score }}" {
		t.Fatalf("env not preserved: %v", merger.Env)
	}
	if !reflect.DeepEqual(merger.DependsOn, []string{"reviewer-a", "reviewer-b"}) {
		t.Fatalf("dependsOn = %v", merger.DependsOn)
	}
}

// Every case here is a mistake that must be caught before a single agent
// starts. The message matters as much as the rejection: a workflow author
// reads it instead of reading the scheduler.
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{{
		name: "no name",
		src:  `{"agents":[{"name":"a","command":["true"]}]}`,
		want: "name is required",
	}, {
		name: "no agents",
		src:  `{"name":"empty","agents":[]}`,
		want: "declares no agents",
	}, {
		name: "no command",
		src:  agents(`{"name":"a"}`),
		want: `agent "a": command is required`,
	}, {
		name: "duplicate agent",
		src:  agents(`{"name":"a","command":["true"]},{"name":"a","command":["true"]}`),
		want: "declared twice",
	}, {
		name: "duplicate output",
		src:  agents(`{"name":"a","command":["true"],"outputs":[{"name":"x"},{"name":"x"}]}`),
		want: `output "x" declared twice`,
	}, {
		// Agent names become task IDs, workspace directories and, on a
		// cluster, object names. Rejecting here beats failing at apply time.
		name: "name is not a DNS label",
		src:  agents(`{"name":"Planner","command":["true"]}`),
		want: "must be a DNS label",
	}, {
		name: "unknown dependency",
		src:  agents(`{"name":"a","command":["true"],"dependsOn":["ghost"]}`),
		want: `dependsOn unknown agent "ghost"`,
	}, {
		name: "self dependency",
		src:  agents(`{"name":"a","command":["true"],"dependsOn":["a"]}`),
		want: "depends on itself",
	}, {
		name: "cycle",
		src: agents(`{"name":"a","command":["true"],"dependsOn":["b"]},` +
			`{"name":"b","command":["true"],"dependsOn":["a"]}`),
		want: "dependency cycle among [a b]",
	}, {
		// encoding/json matches field names case-insensitively, so
		// "dependson" is accepted as dependsOn; a genuinely misspelled key
		// must not be dropped on the floor.
		name: "unknown field is not silently dropped",
		src:  agents(`{"name":"a","command":["true"],"depends_on":["b"]}`),
		want: `unknown field "depends_on"`,
	}, {
		// The headline check: a typo in the output name is caught now, not
		// after the producer has spent ten minutes running.
		name: "reference to an undeclared output",
		src: agents(`{"name":"planner","command":["true"],"outputs":[{"name":"plan"}]},` +
			`{"name":"r","dependsOn":["planner"],"command":["echo","{{ planner.output.plna }}"]}`),
		want: `declares no output "plna"`,
	}, {
		// Reading from an agent you do not depend on is a race, not a
		// shortcut: nothing orders the two.
		name: "reference without a dependency edge",
		src: agents(`{"name":"planner","command":["true"],"outputs":[{"name":"plan"}]},` +
			`{"name":"r","command":["echo","{{ planner.output.plan }}"]}`),
		want: "which it does not depend on",
	}, {
		name: "reference to an unknown agent",
		src:  agents(`{"name":"r","command":["echo","{{ ghost.output.x }}"]}`),
		want: `refers to unknown agent "ghost"`,
	}, {
		name: "reference to an agent that declares nothing",
		src: agents(`{"name":"planner","command":["true"]},` +
			`{"name":"r","dependsOn":["planner"],"command":["echo","{{ planner.output.plan }}"]}`),
		want: "declares no outputs",
	}, {
		name: "malformed reference",
		src:  agents(`{"name":"r","command":["echo","{{ planner.outpt.score }}"]}`),
		want: "malformed reference",
	}, {
		name: "broken reference in env is caught too",
		src:  agents(`{"name":"r","command":["true"],"env":{"X":"{{ ghost.output.x }}"}}`),
		want: `unknown agent "ghost"`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseJSON([]byte(tc.src))
			if err == nil {
				t.Fatal("expected rejection, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsAValidDiamond(t *testing.T) {
	if err := mustParse(t, diamond).Validate(); err != nil {
		t.Fatalf("valid topology rejected: %v", err)
	}
}

func TestTopoOrderRespectsDepsAndIsStable(t *testing.T) {
	topo := mustParse(t, diamond)
	first, err := topo.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"planner", "reviewer-a", "reviewer-b", "merger"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("order = %v, want %v (declaration order breaks ties)", first, want)
	}
	// Determinism is load-bearing: map iteration order must not leak in, or a
	// restarted controller starts the diamond's branches in a different order
	// and two runs of the same workflow stop being comparable.
	for i := 0; i < 20; i++ {
		again, err := topo.TopoOrder()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("order changed between runs: %v then %v", first, again)
		}
	}
}
