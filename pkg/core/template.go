package core

import (
	"fmt"
	"regexp"
	"strings"
)

// The template language is deliberately one rule: {{ agent.output.key }}.
// No conditionals, no functions, no nesting, no arithmetic. A workflow file
// that needs a second rule is a workflow file that should have been a script
// — and every rule added here is a rule Validate must be able to check
// statically, which is the property that catches typos at submit time.
var (
	refRe   = regexp.MustCompile(`^\{\{\s*([a-z0-9](?:[-a-z0-9]*[a-z0-9])?)\.output\.([A-Za-z0-9_.-]+)\s*\}\}$`)
	braceRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)
)

// Ref is one resolved {{ }} occurrence. Raw is kept so errors quote the text
// the user actually wrote.
type Ref struct {
	Agent string
	Key   string
	Raw   string
}

// Refs extracts every reference in s. Anything that looks like a template but
// does not parse is an error rather than literal text: {{ planner.outpt.score }}
// passed through verbatim would reach the agent as an argument and be blamed
// on the agent.
func Refs(s string) ([]Ref, error) {
	var out []Ref
	for _, raw := range braceRe.FindAllString(s, -1) {
		m := refRe.FindStringSubmatch(raw)
		if m == nil {
			return nil, fmt.Errorf("malformed reference %s — the only supported form is {{ agent.output.key }}", raw)
		}
		out = append(out, Ref{Agent: m[1], Key: m[2], Raw: raw})
	}
	return out, nil
}

// Render substitutes every reference in s from observed state.
//
// A missing value is always an error, never an empty string. Substituting ""
// hands the downstream agent "Review result: " and it will confabulate around
// the hole — the same reasoning as OutputMissing on the task side.
func Render(s string, obs Observed) (string, error) {
	refs, err := Refs(s)
	if err != nil {
		return "", err
	}
	out := s
	for _, r := range refs {
		v, ok := obs[r.Agent]
		if !ok {
			return "", fmt.Errorf("%s: agent %q has not run", r.Raw, r.Agent)
		}
		if v.Phase != PhaseSucceeded {
			return "", fmt.Errorf("%s: agent %q is %s — its outputs are not readable yet", r.Raw, r.Agent, v.Phase)
		}
		val, ok := v.Outputs[r.Key]
		if !ok {
			return "", fmt.Errorf("%s: agent %q produced no output %q (it produced %v)",
				r.Raw, r.Agent, r.Key, sortedKeys(v.Outputs))
		}
		out = strings.ReplaceAll(out, r.Raw, val)
	}
	return out, nil
}

// RenderAll renders a command line, preserving argv boundaries: a value that
// contains spaces stays one argument, because substitution happens inside an
// element and never re-splits it.
func RenderAll(ss []string, obs Observed) ([]string, error) {
	if ss == nil {
		return nil, nil
	}
	out := make([]string, 0, len(ss))
	for i, s := range ss {
		r, err := Render(s, obs)
		if err != nil {
			return nil, fmt.Errorf("argv[%d]: %w", i, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// RenderEnv renders environment values. Keys are visited in sorted order so
// that a topology with two broken references always reports the same one.
func RenderEnv(m map[string]string, obs Observed) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		v, err := Render(m[k], obs)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}
