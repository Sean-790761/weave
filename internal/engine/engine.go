// Package engine holds the reconcile loop for a single task.
//
// The whole loop is level-triggered: every transition is derived from
// persisted state alone, never from what happened earlier in this process.
// That is invariant I6 — kill the controller at any point, restart it, and it
// lands in the same place. The demo exercises this by exiting the process
// between every transition.
//
// This is NOT pkg/core. Core is the DAG scheduler and stays a pure function
// over (Topology, ObservedTasks). This package is the platform-side glue that
// Core will sit behind.
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Sean-790761/weave/internal/envelope"
	"github.com/Sean-790761/weave/internal/executor"
	"github.com/Sean-790761/weave/internal/model"
)

type Engine struct {
	Exec  executor.Executor
	Store model.Store
	Now   func() time.Time
	Log   func(format string, args ...any)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// Reconcile advances the task by at most one transition and reports whether
// anything changed. Callers loop until it returns false.
func (e *Engine) Reconcile(ctx context.Context, t *model.Task) (bool, error) {
	switch t.Status.Phase {

	case model.PhaseSucceeded, model.PhaseFailed:
		return false, nil

	case model.PhasePending:
		spec := e.specFor(t)
		h, err := e.Exec.Ensure(ctx, spec)
		if err != nil {
			return false, fmt.Errorf("ensure: %w", err)
		}
		now := e.now()
		if t.Status.StartTime == nil {
			t.Status.StartTime = &now
		}
		t.Status.Phase = model.PhaseRunning
		t.Status.HandleID = h.ID
		e.logf("attempt %d started (%s)", t.Spec.Attempt, h.Runtime)
		return true, nil

	case model.PhaseRunning:
		h := e.handleFor(t)
		st, err := e.Exec.Observe(ctx, h)
		if err != nil {
			return false, fmt.Errorf("observe: %w", err)
		}
		if st.Phase == executor.PhaseRunning {
			return false, nil
		}
		return true, e.apply(ctx, t, h, st)

	case model.PhaseWaitingForUserInput:
		ui := t.Status.UserInput
		if ui == nil || ui.Response == nil {
			return false, nil
		}
		// Answer accepted: bump the attempt, drop every trace of the previous
		// one, and let Pending re-Ensure with the resume value injected.
		t.Spec.Attempt++
		t.Status.Outputs = nil
		t.Status.OutputsAttempt = 0
		t.Status.Phase = model.PhasePending
		t.Status.HandleID = ""
		e.logf("resuming with input %q (requestId %s)", *ui.Response, ui.RequestID)
		return true, nil

	default:
		return false, fmt.Errorf("unknown phase %q", t.Status.Phase)
	}
}

func (e *Engine) specFor(t *model.Task) *executor.ExecSpec {
	env := make(map[string]string, len(t.Spec.Env)+5)
	for k, v := range t.Spec.Env {
		env[k] = v
	}
	// Reserved names are written last on purpose: a topology that sets
	// WEAVE_RESUME_INPUT itself must not be able to fake an answer.
	env["WEAVE_RUN"] = t.Spec.RunRef
	env["WEAVE_AGENT"] = t.Spec.AgentName
	env["WEAVE_ITEM"] = t.Spec.ItemID
	if ui := t.Status.UserInput; ui != nil && ui.Response != nil {
		env["WEAVE_RESUME_INPUT"] = *ui.Response
		env["WEAVE_RESUME_REQUEST_ID"] = ui.RequestID
	}
	return &executor.ExecSpec{
		ID:            t.ID,
		Image:         t.Spec.Image,
		Command:       t.Spec.Command,
		Env:           env,
		WorkspacePath: e.Store.Workspace(),
		Attempt:       t.Spec.Attempt,
	}
}

func (e *Engine) handleFor(t *model.Task) *executor.Handle {
	return &executor.Handle{
		ID:      t.Status.HandleID,
		Attempt: t.Spec.Attempt,
		Ref:     e.Store.Workspace(),
	}
}

func (e *Engine) apply(ctx context.Context, t *model.Task, h *executor.Handle, st *executor.State) error {
	now := e.now()

	if st.ParseError != nil {
		return e.fail(t, envelope.ReasonContractViolation,
			fmt.Sprintf("exit %d produced no readable envelope: %v", st.ExitCode, st.ParseError))
	}
	env := st.Envelope

	// A stale envelope from an earlier attempt must never be believed.
	if env.Attempt != t.Spec.Attempt {
		return e.fail(t, envelope.ReasonContractViolation,
			fmt.Sprintf("envelope is for attempt %d, task is on attempt %d", env.Attempt, t.Spec.Attempt))
	}

	switch env.Kind {

	case envelope.KindDone:
		if missing := missingRequired(t.Spec.Outputs, env.Outputs); len(missing) > 0 {
			// Deliberately a failure, not an empty string. A silently blank
			// output reaches the downstream agent as "审查结果: " and it will
			// confabulate around the hole.
			return e.fail(t, envelope.ReasonOutputMissing,
				fmt.Sprintf("agent declared outputs %v but did not produce them", missing))
		}
		t.Status.Outputs = env.Outputs
		t.Status.OutputsAttempt = t.Spec.Attempt
		t.Status.Phase = model.PhaseSucceeded
		t.Status.FinishTime = &now
		t.Status.HandleID = ""
		e.logf("succeeded with outputs %v", keys(env.Outputs))
		return nil

	case envelope.KindWaiting:
		// Release the workload, keep its durable state. This is where the Pod
		// dies and the PVC survives.
		if err := e.Exec.Cancel(ctx, h); err != nil {
			return fmt.Errorf("cancel: %w", err)
		}
		t.Status.UserInput = &model.UserInput{
			Prompt:    env.Prompt,
			RequestID: env.RequestID,
			AskedAt:   now,
		}
		t.Status.Phase = model.PhaseWaitingForUserInput
		t.Status.HandleID = ""
		e.logf("waiting for input: %s (requestId %s)", env.Prompt, env.RequestID)
		return nil

	case envelope.KindFailed:
		return e.fail(t, env.Reason, env.Message)
	}
	return fmt.Errorf("unhandled envelope kind %q", env.Kind)
}

func (e *Engine) fail(t *model.Task, reason, msg string) error {
	now := e.now()
	t.Status.Phase = model.PhaseFailed
	t.Status.FailureReason = reason
	t.Status.FailureMessage = msg
	t.Status.FinishTime = &now
	t.Status.HandleID = ""
	e.logf("failed: %s — %s", reason, msg)
	return nil
}

// Answer records a human response. It is the guard against the double-send
// race: user sends, the network times out, the user retries — meanwhile the
// agent has resumed and asked a different question. Without the requestId
// check the retry answers the wrong prompt.
func Answer(t *model.Task, requestID, response string, now time.Time) error {
	if t.Status.Phase != model.PhaseWaitingForUserInput {
		return fmt.Errorf("task is %s, not waiting for input", t.Status.Phase)
	}
	ui := t.Status.UserInput
	if ui == nil {
		return fmt.Errorf("task is waiting but carries no question")
	}
	if ui.Response != nil {
		return fmt.Errorf("question %s was already answered at %s", ui.RequestID, ui.RespondedAt.Format(time.RFC3339))
	}
	if requestID != "" && requestID != ui.RequestID {
		return fmt.Errorf("stale answer: you are answering %s but the task is now asking %s",
			requestID, ui.RequestID)
	}
	ui.Response = &response
	ui.RespondedAt = &now
	return nil
}

// Run drives Reconcile until the task stops moving, persisting after every
// transition so a crash between any two steps is recoverable.
func (e *Engine) Run(ctx context.Context, t *model.Task) error {
	for {
		changed, err := e.Reconcile(ctx, t)
		if err != nil {
			return err
		}
		if changed {
			if serr := e.Store.Save(t); serr != nil {
				return serr
			}
		}
		if !changed {
			return nil
		}
		if t.Status.Phase.Terminal() || t.Status.Phase == model.PhaseWaitingForUserInput {
			return nil
		}
	}
}

func missingRequired(decls []model.OutputDecl, got map[string]string) []string {
	var missing []string
	for _, d := range decls {
		if !d.Required {
			continue
		}
		if _, ok := got[d.Name]; !ok {
			missing = append(missing, d.Name)
		}
	}
	return missing
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
