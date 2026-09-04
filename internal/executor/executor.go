package executor

import (
	"context"
	"errors"

	"github.com/Sean-790761/weave/internal/envelope"
)

var ErrNotSupported = errors.New("operation not supported by this executor")

// Capabilities lets the controller negotiate rather than assume. A backend
// that can freeze a workload (agent-sandbox pause, CRIU, Firecracker
// snapshot) can implement WaitingForUserInput without killing the process;
// one that cannot falls back to Cancel + workspace reuse.
type Capabilities struct {
	Pause    bool
	Snapshot bool
	WarmPool bool
}

// ExecSpec is deliberately free of Kubernetes types. If a PodSpec, a PVC name
// or a RuntimeClass ever appears in this struct, the abstraction is fake and
// the second executor implementation will not fit.
type ExecSpec struct {
	ID            string
	Image         string
	Command       []string
	Env           map[string]string
	WorkspacePath string
	Attempt       int
}

type Handle struct {
	ID      string
	Runtime string
	Attempt int
	// Ref is an opaque, executor-defined pointer to the durable state behind
	// this workload (a workspace path locally, a PVC name on Kubernetes).
	// Keeping it opaque is what stops PVC semantics leaking into the engine.
	Ref string
}

type Phase string

const (
	PhaseRunning    Phase = "Running"
	PhaseTerminated Phase = "Terminated"
	PhaseGone       Phase = "Gone"
)

type State struct {
	Phase    Phase
	ExitCode int
	Envelope *envelope.Envelope
	// ParseError is set when a workload terminated but produced no readable
	// envelope, e.g. the node died before the kubelet recorded it.
	ParseError error
}

type Executor interface {
	Ensure(ctx context.Context, spec *ExecSpec) (*Handle, error)
	Observe(ctx context.Context, h *Handle) (*State, error)
	Cancel(ctx context.Context, h *Handle) error
	Pause(ctx context.Context, h *Handle) error
	Resume(ctx context.Context, h *Handle, env map[string]string) error
	Capabilities() Capabilities
}
