package core

import "fmt"

// Phase mirrors model.Phase by value. Core cannot import the platform side
// (that is the whole point of the split), so the two lists are kept equal by
// a test in the wiring layer rather than by the compiler.
type Phase string

const (
	PhasePending             Phase = "Pending"
	PhaseRunning             Phase = "Running"
	PhaseWaitingForUserInput Phase = "WaitingForUserInput"
	PhaseSucceeded           Phase = "Succeeded"
	PhaseFailed              Phase = "Failed"
)

// Terminal reports whether a phase can still change on its own.
func (p Phase) Terminal() bool { return p == PhaseSucceeded || p == PhaseFailed }

// TaskView is what the platform saw for one node. It is a snapshot, not a
// handle: Core never reaches back into the store.
type TaskView struct {
	Agent          string
	Phase          Phase
	Outputs        map[string]string
	FailureReason  string
	FailureMessage string
}

// Observed is every task the platform knows about, keyed by agent name.
// An agent absent from the map has not been started yet — that absence is the
// only "has it started?" signal Core uses, which is what keeps it
// level-triggered.
//
// One task per agent for now. Fan-out will key this by agent+item; nothing
// else in Core changes, because the scheduling rules never assume one.
type Observed map[string]TaskView

// ActionKind is what the platform should do next.
type ActionKind string

const (
	// ActionStartTask creates a task for Agent with Command/Env already
	// rendered. The platform never sees a {{ }}.
	ActionStartTask ActionKind = "StartTask"
	// ActionRunSucceeded means every agent finished successfully.
	ActionRunSucceeded ActionKind = "RunSucceeded"
	// ActionRunFailed means one agent failed and the run cannot complete.
	ActionRunFailed ActionKind = "RunFailed"
)

// Action is an instruction, not a mutation: Core returns it, the platform
// performs it. That is what makes the scheduling rules table-testable.
type Action struct {
	Kind    ActionKind
	Agent   string
	Image   string
	Command []string
	Env     map[string]string
	Outputs []OutputDecl
	Reason  string
	Message string
}

// Decide returns everything that should happen next, given what was observed.
//
// The contract:
//
//   - Fail fast. One failed agent ends the run; nothing new is started, and
//     the result is exactly one RunFailed naming the culprit.
//   - Start what is ready. An agent starts when it has no task yet and every
//     dependency has Succeeded. Several may become ready at once; they are
//     returned in topology order.
//   - A blocked branch does not block the others. An agent parked in
//     WaitingForUserInput holds up its own dependents and nothing else.
//   - Idempotent. Calling Decide twice on the same Observed returns the same
//     Actions; the platform is expected to re-derive rather than remember.
func Decide(t *Topology, obs Observed) ([]Action, error) {
	// Fail fast, in topology order so the reported culprit is the earliest
	// one rather than whichever map iteration happened to reach first.
	for _, a := range t.Agents {
		if v, ok := obs[a.Name]; ok && v.Phase == PhaseFailed {
			return []Action{{
				Kind:    ActionRunFailed,
				Agent:   a.Name,
				Reason:  v.FailureReason,
				Message: v.FailureMessage,
			}}, nil
		}
	}

	allSucceeded := true
	var start []Action
	for _, a := range t.Agents {
		v, started := obs[a.Name]
		if started && v.Phase == PhaseSucceeded {
			continue
		}
		allSucceeded = false
		if started {
			// Pending / Running / WaitingForUserInput: the task exists, so
			// there is nothing for Core to do about it.
			continue
		}
		if !depsSucceeded(&a, obs) {
			continue
		}
		cmd, err := RenderAll(a.Command, obs)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", a.Name, err)
		}
		env, err := RenderEnv(a.Env, obs)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", a.Name, err)
		}
		start = append(start, Action{
			Kind:    ActionStartTask,
			Agent:   a.Name,
			Image:   a.Image,
			Command: cmd,
			Env:     env,
			Outputs: a.Outputs,
		})
	}

	if allSucceeded {
		return []Action{{Kind: ActionRunSucceeded}}, nil
	}
	return start, nil
}

// RunPhase collapses the observed tasks into the phase of the run as a whole.
// It is derived, never stored: the run's phase is always a function of its
// tasks, so there is no way for the two to disagree after a crash.
func RunPhase(t *Topology, obs Observed) Phase {
	var running, waiting, succeeded int
	for _, a := range t.Agents {
		v, ok := obs[a.Name]
		if !ok {
			continue
		}
		switch v.Phase {
		case PhaseFailed:
			return PhaseFailed
		case PhaseSucceeded:
			succeeded++
		case PhaseWaitingForUserInput:
			waiting++
		default:
			running++
		}
	}
	switch {
	case succeeded == len(t.Agents):
		return PhaseSucceeded
	case running > 0:
		return PhaseRunning
	case waiting > 0:
		// Nothing is executing and at least one agent is parked on a
		// question: the run itself is what is waiting for the human.
		return PhaseWaitingForUserInput
	case succeeded > 0:
		return PhaseRunning
	default:
		return PhasePending
	}
}

func depsSucceeded(a *Agent, obs Observed) bool {
	for _, d := range a.DependsOn {
		if v, ok := obs[d]; !ok || v.Phase != PhaseSucceeded {
			return false
		}
	}
	return true
}
