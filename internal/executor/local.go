package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Sean-790761/weave/internal/envelope"
)

// Local runs the agent as a child process instead of a Pod. It re-execs the
// weave binary in shim mode so the production entrypoint path is exercised
// verbatim — the only thing that changes is what wraps the shim.
//
// Local is synchronous: Ensure returns once the agent has exited. A Pod-backed
// executor will be asynchronous and Observe will do real work. Both satisfy
// the same interface because Ensure is specified as idempotent-per-attempt,
// not as "start".
type Local struct {
	Bin string // path to the weave binary; defaults to os.Executable()
}

func (l *Local) Capabilities() Capabilities { return Capabilities{} }

func attemptDir(ws string, attempt int) string {
	return filepath.Join(ws, ".weave", "attempts", strconv.Itoa(attempt))
}

func (l *Local) Ensure(ctx context.Context, spec *ExecSpec) (*Handle, error) {
	h := &Handle{
		ID:      fmt.Sprintf("%s#%d", spec.ID, spec.Attempt),
		Runtime: "local",
		Attempt: spec.Attempt,
		Ref:     spec.WorkspacePath,
	}

	dir := attemptDir(spec.WorkspacePath, spec.Attempt)
	if _, err := os.Stat(filepath.Join(dir, "result.json")); err == nil {
		return h, nil // already ran this attempt; Ensure is level-triggered
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(spec.WorkspacePath, 0o755); err != nil {
		return nil, err
	}

	bin := l.Bin
	if bin == "" {
		p, err := os.Executable()
		if err != nil {
			return nil, err
		}
		bin = p
	}

	termLog := filepath.Join(dir, "termination-log")
	args := append([]string{"shim", "--"}, spec.Command...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"WEAVE_WORKSPACE="+spec.WorkspacePath,
		"WEAVE_TERMINATION_LOG="+termLog,
		"WEAVE_ATTEMPT="+strconv.Itoa(spec.Attempt),
	)
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if ok := asExitError(runErr, &ee); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}

	res, _ := json.Marshal(map[string]int{"exitCode": code})
	if err := os.WriteFile(filepath.Join(dir, "result.json"), res, 0o644); err != nil {
		return nil, err
	}
	return h, nil
}

func (l *Local) Observe(ctx context.Context, h *Handle) (*State, error) {
	if h.Ref == "" {
		return nil, fmt.Errorf("local executor: handle has no workspace ref")
	}
	dir := attemptDir(h.Ref, h.Attempt)

	b, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return &State{Phase: PhaseRunning}, nil
	}
	var res struct {
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, err
	}

	st := &State{Phase: PhaseTerminated, ExitCode: res.ExitCode}
	raw, err := os.ReadFile(filepath.Join(dir, "termination-log"))
	if err != nil {
		st.ParseError = fmt.Errorf("no termination message recorded: %w", err)
		return st, nil
	}
	env, err := envelope.Parse(raw)
	if err != nil {
		st.ParseError = err
		return st, nil
	}
	st.Envelope = env
	return st, nil
}

// Cancel is a no-op for the synchronous executor: the child has already
// exited by the time the engine asks. The workspace is deliberately left in
// place — that is the local stand-in for "delete the Pod, keep the PVC".
func (l *Local) Cancel(ctx context.Context, h *Handle) error { return nil }

func (l *Local) Pause(ctx context.Context, h *Handle) error { return ErrNotSupported }

func (l *Local) Resume(ctx context.Context, h *Handle, env map[string]string) error {
	return ErrNotSupported
}

func asExitError(err error, out **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*out = ee
		return true
	}
	return false
}
