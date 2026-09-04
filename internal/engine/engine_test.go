package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/Sean-790761/weave/internal/envelope"
	"github.com/Sean-790761/weave/internal/model"
)

func waiting(reqID string) *model.Task {
	return &model.Task{
		ID:   "t",
		Spec: model.TaskSpec{Attempt: 1},
		Status: model.TaskStatus{
			Phase:     model.PhaseWaitingForUserInput,
			UserInput: &model.UserInput{Prompt: "a/b/c", RequestID: reqID},
		},
	}
}

func TestAnswerRejectsStaleRequestID(t *testing.T) {
	task := waiting("q2")
	// The user's client last saw q1; meanwhile the agent resumed and asked q2.
	if err := Answer(task, "q1", "a", time.Now()); err == nil {
		t.Fatal("expected stale answer to be rejected")
	}
	if task.Status.UserInput.Response != nil {
		t.Fatal("stale answer must not be recorded")
	}
}

func TestAnswerIsNotIdempotentlyOverwritten(t *testing.T) {
	task := waiting("q1")
	now := time.Now()
	if err := Answer(task, "q1", "a", now); err != nil {
		t.Fatal(err)
	}
	if err := Answer(task, "q1", "b", now); err == nil {
		t.Fatal("expected second answer to the same question to be rejected")
	}
	if *task.Status.UserInput.Response != "a" {
		t.Fatalf("answer mutated to %q", *task.Status.UserInput.Response)
	}
}

func TestAnswerRejectedWhenNotWaiting(t *testing.T) {
	task := waiting("q1")
	task.Status.Phase = model.PhaseRunning
	if err := Answer(task, "", "a", time.Now()); err == nil {
		t.Fatal("expected answer to a running task to be rejected")
	}
}

func TestMissingRequiredOutputs(t *testing.T) {
	decls := []model.OutputDecl{{Name: "score", Required: true}, {Name: "note"}}
	got := missingRequired(decls, map[string]string{"note": "x"})
	if len(got) != 1 || got[0] != "score" {
		t.Fatalf("got %v", got)
	}
	if len(missingRequired(decls, map[string]string{"score": "1"})) != 0 {
		t.Fatal("satisfied requirement reported as missing")
	}
}

func TestOversizeEnvelopeDegradesInsteadOfTruncating(t *testing.T) {
	e := &envelope.Envelope{
		V: envelope.Version, Kind: envelope.KindDone, Attempt: 1,
		Outputs: map[string]string{"report": strings.Repeat("x", envelope.MaxSize*2)},
	}
	b := e.Marshal()
	if len(b) > envelope.MaxSize {
		t.Fatalf("marshalled %d bytes, over budget", len(b))
	}
	got, err := envelope.Parse(b)
	if err != nil {
		t.Fatalf("degraded envelope must still parse: %v", err)
	}
	if got.Reason != envelope.ReasonOutputTooLarge {
		t.Fatalf("reason = %q", got.Reason)
	}
}
