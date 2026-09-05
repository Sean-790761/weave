// Package core is the DAG scheduler.
//
// Core is a pure function over (Topology, Observed): given the workflow that
// was submitted and what the platform last saw, it returns the Actions to
// take. No IO, no clock, no goroutines, no Kubernetes types, no on-disk
// store. internal/engine observes the world and carries the Actions out.
//
// Two properties fall out of that, and the tests pin both down:
//
//   - Deterministic. The same (Topology, Observed) always yields the same
//     Actions in the same order, so a controller that crashes between any two
//     transitions re-derives the same decision on restart (invariant I6).
//   - Level-triggered. Decide never consults history; a task that is already
//     started is simply absent from the set it proposes to start.
//
// Phase below mirrors model.Phase by value rather than importing it — Core
// must not depend on the platform side. The wiring layer casts between the
// two and a test there pins the lists together.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// dnsLabel is the K8s object-name shape. Agent names become task IDs,
// workspace directory names and, on a cluster, WeaveTask names — so the
// constraint is applied at submit time rather than discovered at apply time.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// OutputDecl declares one value an agent promises to publish. The point is
// not type checking; it is giving Validate something to check, so that a typo
// in {{ planner.output.scoer }} fails at submit time instead of after planner
// has already run.
type OutputDecl struct {
	Name     string `json:"name" yaml:"name"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

// Agent is one node of the DAG. Command and Env values may carry
// {{ producer.output.key }} references to any agent this one depends on.
type Agent struct {
	Name      string            `json:"name" yaml:"name"`
	Image     string            `json:"image,omitempty" yaml:"image,omitempty"`
	Command   []string          `json:"command" yaml:"command"`
	Env       map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
	Outputs   []OutputDecl      `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

// Topology is the whole workflow: the thing a user submits, and the thing
// that becomes WeaveRun.spec.topology on a cluster.
type Topology struct {
	Name   string  `json:"name" yaml:"name"`
	Agents []Agent `json:"agents" yaml:"agents"`
}

// ParseJSON decodes and validates a topology.
//
// JSON is the wire form: it is what an API server hands down once Topology is
// a CRD field, and it keeps Core dependency-free. A front end for
// hand-written files is a thin adapter over this — it belongs at the edge,
// not in the scheduler.
//
// Unknown fields are an error. A "dependson" typo silently dropped is exactly
// the bug that only ever shows up as "why did these two agents run at the
// same time".
func ParseJSON(b []byte) (*Topology, error) {
	var t Topology
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("parse topology: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Agent returns the named node.
func (t *Topology) Agent(name string) (*Agent, bool) {
	for i := range t.Agents {
		if t.Agents[i].Name == name {
			return &t.Agents[i], true
		}
	}
	return nil, false
}

// Validate rejects a topology that could not run correctly. Everything it
// checks is checkable before a single agent starts; anything that needs
// runtime state belongs in Decide, not here.
func (t *Topology) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("topology: name is required")
	}
	if len(t.Agents) == 0 {
		return fmt.Errorf("topology %q: declares no agents", t.Name)
	}

	seen := make(map[string]bool, len(t.Agents))
	for i := range t.Agents {
		a := &t.Agents[i]
		switch {
		case a.Name == "":
			return fmt.Errorf("agents[%d]: name is required", i)
		case !dnsLabel.MatchString(a.Name):
			return fmt.Errorf("agent %q: name must be a DNS label (lowercase alphanumeric and '-')", a.Name)
		case len(a.Name) > 63:
			return fmt.Errorf("agent %q: name is longer than 63 characters", a.Name)
		case seen[a.Name]:
			return fmt.Errorf("agent %q: declared twice", a.Name)
		}
		seen[a.Name] = true

		if len(a.Command) == 0 {
			return fmt.Errorf("agent %q: command is required", a.Name)
		}
		outSeen := make(map[string]bool, len(a.Outputs))
		for j, o := range a.Outputs {
			if o.Name == "" {
				return fmt.Errorf("agent %q: outputs[%d] has no name", a.Name, j)
			}
			if outSeen[o.Name] {
				return fmt.Errorf("agent %q: output %q declared twice", a.Name, o.Name)
			}
			outSeen[o.Name] = true
		}
	}

	for _, a := range t.Agents {
		depSeen := make(map[string]bool, len(a.DependsOn))
		for _, d := range a.DependsOn {
			switch {
			case d == a.Name:
				return fmt.Errorf("agent %q: depends on itself", a.Name)
			case !seen[d]:
				return fmt.Errorf("agent %q: dependsOn unknown agent %q", a.Name, d)
			case depSeen[d]:
				return fmt.Errorf("agent %q: dependsOn %q listed twice", a.Name, d)
			}
			depSeen[d] = true
		}
	}

	if _, err := t.TopoOrder(); err != nil {
		return err
	}
	return t.validateRefs()
}

// validateRefs is where the payoff sits: a {{ }} reference must name an agent
// this one actually depends on, and a key that agent actually declares.
// Without the dependency check the reference is a race — the producer may not
// have run — and without the declaration check a typo only surfaces once the
// producer has finished.
func (t *Topology) validateRefs() error {
	for _, a := range t.Agents {
		ancestors := t.ancestors(a.Name)
		for _, s := range a.templated() {
			refs, err := Refs(s)
			if err != nil {
				return fmt.Errorf("agent %q: %w", a.Name, err)
			}
			for _, r := range refs {
				producer, ok := t.Agent(r.Agent)
				if !ok {
					return fmt.Errorf("agent %q: %s refers to unknown agent %q", a.Name, r.Raw, r.Agent)
				}
				if !ancestors[r.Agent] {
					return fmt.Errorf("agent %q: %s reads from %q, which it does not depend on "+
						"(add %q to dependsOn, or the value may not exist yet)", a.Name, r.Raw, r.Agent, r.Agent)
				}
				if len(producer.Outputs) == 0 {
					return fmt.Errorf("agent %q: %s reads from %q, which declares no outputs",
						a.Name, r.Raw, r.Agent)
				}
				declared := false
				for _, o := range producer.Outputs {
					if o.Name == r.Key {
						declared = true
						break
					}
				}
				if !declared {
					return fmt.Errorf("agent %q: %s — agent %q declares no output %q (it declares %v)",
						a.Name, r.Raw, r.Agent, r.Key, declaredNames(producer.Outputs))
				}
			}
		}
	}
	return nil
}

// templated returns every string on the agent that may carry a reference, in
// a deterministic order so that error messages are stable.
func (a *Agent) templated() []string {
	out := append([]string(nil), a.Command...)
	for _, k := range sortedKeys(a.Env) {
		out = append(out, a.Env[k])
	}
	return out
}

// ancestors is the transitive dependsOn closure. Only safe to call once
// TopoOrder has proved there is no cycle.
func (t *Topology) ancestors(name string) map[string]bool {
	seen := make(map[string]bool)
	var walk func(string)
	walk = func(n string) {
		a, ok := t.Agent(n)
		if !ok {
			return
		}
		for _, d := range a.DependsOn {
			if seen[d] {
				continue
			}
			seen[d] = true
			walk(d)
		}
	}
	walk(name)
	return seen
}

// TopoOrder returns the agents in dependency order, breaking ties by
// declaration order so the result is stable across runs. It is also the cycle
// check: a cycle leaves nodes with indegree > 0 and no way to make progress.
func (t *Topology) TopoOrder() ([]string, error) {
	indeg := make(map[string]int, len(t.Agents))
	dependents := make(map[string][]string, len(t.Agents))
	for _, a := range t.Agents {
		indeg[a.Name] += len(a.DependsOn)
		for _, d := range a.DependsOn {
			dependents[d] = append(dependents[d], a.Name)
		}
	}

	order := make([]string, 0, len(t.Agents))
	done := make(map[string]bool, len(t.Agents))
	for len(order) < len(t.Agents) {
		progressed := false
		for _, a := range t.Agents { // declaration order = deterministic tie-break
			if done[a.Name] || indeg[a.Name] > 0 {
				continue
			}
			done[a.Name] = true
			order = append(order, a.Name)
			for _, dep := range dependents[a.Name] {
				indeg[dep]--
			}
			progressed = true
		}
		if !progressed {
			var stuck []string
			for _, a := range t.Agents {
				if !done[a.Name] {
					stuck = append(stuck, a.Name)
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("topology %q: dependency cycle among %v", t.Name, stuck)
		}
	}
	return order, nil
}

func declaredNames(decls []OutputDecl) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
