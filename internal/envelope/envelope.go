// Package envelope defines the single wire format between an agent container
// and the Weave controller.
//
// The envelope travels through the container's termination message
// (/dev/termination-log -> pod.status.containerStatuses[].state.terminated.message),
// NOT through stdout. Rationale: stdout is the log plane, it is lossy, it is
// gone once the pod is deleted, and agent frameworks write arbitrary noise to
// it. The termination message is persisted in etcd as part of pod status and
// survives the pod deletion that WaitingForUserInput performs.
//
// Hard limit is 4KiB, enforced by the kubelet. See ReasonOutputTooLarge.
package envelope

import (
	"encoding/json"
	"fmt"
)

const (
	// Version is the envelope schema version. Bump on breaking changes.
	Version = 1

	// MaxSize is the kubelet's termination message limit.
	MaxSize = 4096

	// ExitAsk is the exit code an agent uses to request human input.
	ExitAsk = 77
)

type Kind string

const (
	KindDone    Kind = "done"
	KindWaiting Kind = "waiting"
	KindFailed  Kind = "failed"
)

// Failure reasons. Transient/Error/Timeout mirror the Core classification;
// the rest are contract violations detected by the shim or controller.
const (
	ReasonTransient         = "Transient"
	ReasonError             = "Error"
	ReasonTimeout           = "Timeout"
	ReasonOutputTooLarge    = "OutputTooLarge"
	ReasonOutputMissing     = "OutputMissing"
	ReasonContractViolation = "ContractViolation"
)

// Envelope is what the shim writes on child exit. Exactly one Kind per exit.
type Envelope struct {
	V       int  `json:"v"`
	Kind    Kind `json:"kind"`
	Attempt int  `json:"attempt"`

	// KindDone. Values are raw strings for scalars; structured values are
	// stored as their compact JSON text. Keeping this map[string]string
	// keeps the CRD schema flat and the etcd diff small.
	Outputs map[string]string `json:"outputs,omitempty"`

	// KindWaiting.
	Prompt    string `json:"prompt,omitempty"`
	RequestID string `json:"requestId,omitempty"`

	// KindFailed.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *Envelope) Validate() error {
	if e.V != Version {
		return fmt.Errorf("unsupported envelope version %d (want %d)", e.V, Version)
	}
	switch e.Kind {
	case KindDone:
	case KindWaiting:
		if e.Prompt == "" {
			return fmt.Errorf("waiting envelope has empty prompt")
		}
		if e.RequestID == "" {
			return fmt.Errorf("waiting envelope has empty requestId")
		}
	case KindFailed:
		if e.Reason == "" {
			return fmt.Errorf("failed envelope has empty reason")
		}
	default:
		return fmt.Errorf("unknown envelope kind %q", e.Kind)
	}
	return nil
}

// Marshal serialises and enforces the 4KiB budget. On overflow it does NOT
// truncate: a truncated JSON payload produces a parse error downstream whose
// message points nowhere near the real cause. It degrades to a failure
// envelope instead, so the error is attributable.
func (e *Envelope) Marshal() []byte {
	b, err := json.Marshal(e)
	if err != nil {
		b, _ = json.Marshal(&Envelope{
			V: Version, Kind: KindFailed, Attempt: e.Attempt,
			Reason: ReasonContractViolation, Message: "envelope not serialisable: " + err.Error(),
		})
		return b
	}
	if len(b) > MaxSize {
		over, _ := json.Marshal(&Envelope{
			V: Version, Kind: KindFailed, Attempt: e.Attempt,
			Reason: ReasonOutputTooLarge,
			Message: fmt.Sprintf(
				"envelope is %d bytes, limit is %d; pass artifacts by URI, not by value",
				len(b), MaxSize),
		})
		return over
	}
	return b
}

func Parse(b []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("parse envelope: %w", err)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return &e, nil
}
