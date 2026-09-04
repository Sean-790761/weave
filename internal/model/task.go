// Package model holds the runtime state. The Go structs here mirror
// WeaveTask.status field-for-field; the file-backed Store is a stand-in for
// etcd so the scheduling logic can be exercised without a cluster.
package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Phase string

const (
	PhasePending             Phase = "Pending"
	PhaseRunning             Phase = "Running"
	PhaseWaitingForUserInput Phase = "WaitingForUserInput"
	PhaseSucceeded           Phase = "Succeeded"
	PhaseFailed              Phase = "Failed"
)

func (p Phase) Terminal() bool { return p == PhaseSucceeded || p == PhaseFailed }

// UserInput carries one question and its answer. RequestID lives here rather
// than only on the Run: `weave send` must target a specific question, or a
// retried send can answer a question the agent has already moved past.
type UserInput struct {
	Prompt      string     `json:"prompt"`
	RequestID   string     `json:"requestId"`
	Response    *string    `json:"response"`
	RespondedAt *time.Time `json:"respondedAt"`
	AskedAt     time.Time  `json:"askedAt"`
}

type OutputDecl struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

type TaskSpec struct {
	RunRef    string       `json:"runRef"`
	AgentName string       `json:"agentName"`
	ItemID    string       `json:"itemID,omitempty"`
	Attempt   int          `json:"attempt"`
	Image     string       `json:"image"`
	Command   []string     `json:"command"`
	Outputs   []OutputDecl `json:"outputs,omitempty"`
}

type TaskStatus struct {
	Phase   Phase             `json:"phase"`
	Outputs map[string]string `json:"outputs,omitempty"`
	// OutputsAttempt guards against a later attempt reading an earlier
	// attempt's residue. A mismatch means the outputs are not valid.
	OutputsAttempt int        `json:"outputsAttempt,omitempty"`
	UserInput      *UserInput `json:"userInput,omitempty"`
	FailureReason  string     `json:"failureReason,omitempty"`
	FailureMessage string     `json:"failureMessage,omitempty"`
	StartTime      *time.Time `json:"startTime,omitempty"`
	FinishTime     *time.Time `json:"finishTime,omitempty"`
	HandleID       string     `json:"handleID,omitempty"`
}

type Task struct {
	ID              string     `json:"id"`
	Spec            TaskSpec   `json:"spec"`
	Status          TaskStatus `json:"status"`
	ResourceVersion int        `json:"resourceVersion"`
}

func NewTask(id, agent string, command []string, decls []OutputDecl) *Task {
	return &Task{
		ID: id,
		Spec: TaskSpec{
			AgentName: agent,
			Attempt:   1,
			Command:   command,
			Outputs:   decls,
		},
		Status: TaskStatus{Phase: PhasePending},
	}
}

// Store persists a Task as JSON. Writes are atomic (tmp + rename) so a crash
// mid-write cannot leave a half-parsed state file — the local analogue of an
// etcd transaction.
type Store struct{ Dir string }

func (s Store) path() string { return filepath.Join(s.Dir, "task.json") }

func (s Store) Load() (*Task, error) {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("corrupt task state: %w", err)
	}
	return &t, nil
}

func (s Store) Save(t *Task) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	t.ResourceVersion++
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func (s Store) Exists() bool {
	_, err := os.Stat(s.path())
	return err == nil
}

func (s Store) Workspace() string { return filepath.Join(s.Dir, "workspace") }
