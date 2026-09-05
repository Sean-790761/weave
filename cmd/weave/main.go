package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Sean-790761/weave/internal/engine"
	"github.com/Sean-790761/weave/internal/executor"
	"github.com/Sean-790761/weave/internal/model"
	"github.com/Sean-790761/weave/internal/shim"
)

const usage = `weave — spike build (single task, no DAG)

  weave shim -- <cmd>...            entrypoint wrapper; emits the envelope
  weave run    --dir D -- <cmd>...  reconcile until terminal or waiting
  weave send   --dir D --input V    answer the pending question
  weave status --dir D [--json]     show task state

shim flags:
  --termination-log path     where to write the envelope
                             (default /dev/termination-log; env WEAVE_TERMINATION_LOG)

run flags:
  --output name[:required]   declare an output (repeatable)
  --agent  name              agent name recorded on the task
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "shim":
		os.Exit(shim.Run(shim.ConfigFromEnv(), afterDoubleDash(os.Args[2:])))
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "send":
		os.Exit(cmdSend(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func afterDoubleDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func beforeDoubleDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[:i]
		}
	}
	return args
}

type outputFlags []model.OutputDecl

func (o *outputFlags) String() string { return "" }
func (o *outputFlags) Set(v string) error {
	name, rest, _ := strings.Cut(v, ":")
	if name == "" {
		return fmt.Errorf("empty output name")
	}
	*o = append(*o, model.OutputDecl{Name: name, Required: rest == "required"})
	return nil
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", "", "task directory (state + workspace)")
	agent := fs.String("agent", "agent", "agent name")
	var outs outputFlags
	fs.Var(&outs, "output", "declared output, name[:required]")
	_ = fs.Parse(beforeDoubleDash(args))

	cmd := afterDoubleDash(args)
	if *dir == "" {
		return die("--dir is required")
	}

	store := model.Store{Dir: *dir}
	var t *model.Task
	if store.Exists() {
		var err error
		if t, err = store.Load(); err != nil {
			return die("%v", err)
		}
	} else {
		if len(cmd) == 0 {
			return die("first run needs the agent command after --")
		}
		t = model.NewTask(*agent+"-0", *agent, cmd, outs)
		if err := store.Save(t); err != nil {
			return die("%v", err)
		}
	}

	e := &engine.Engine{
		Exec:  &executor.Local{},
		Store: store,
		Log:   func(f string, a ...any) { fmt.Printf("[weave] "+f+"\n", a...) },
	}
	if err := e.Run(context.Background(), t); err != nil {
		return die("%v", err)
	}
	printStatus(t)
	if t.Status.Phase == model.PhaseFailed {
		return 1
	}
	return 0
}

func cmdSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	dir := fs.String("dir", "", "task directory")
	input := fs.String("input", "", "the answer")
	reqID := fs.String("request-id", "", "question this answers (recommended)")
	_ = fs.Parse(args)

	if *dir == "" || *input == "" {
		return die("--dir and --input are required")
	}
	store := model.Store{Dir: *dir}
	t, err := store.Load()
	if err != nil {
		return die("%v", err)
	}
	if err := engine.Answer(t, *reqID, *input, time.Now().UTC()); err != nil {
		return die("%v", err)
	}
	if err := store.Save(t); err != nil {
		return die("%v", err)
	}
	fmt.Printf("[weave] recorded answer %q for requestId %s\n", *input, t.Status.UserInput.RequestID)
	return 0
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", "", "task directory")
	asJSON := fs.Bool("json", false, "raw state")
	_ = fs.Parse(args)

	store := model.Store{Dir: *dir}
	t, err := store.Load()
	if err != nil {
		return die("%v", err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	printStatus(t)
	return 0
}

func printStatus(t *model.Task) {
	fmt.Printf("\n  task     %s\n", t.ID)
	fmt.Printf("  phase    %s\n", t.Status.Phase)
	fmt.Printf("  attempt  %d   (resourceVersion %d)\n", t.Spec.Attempt, t.ResourceVersion)

	if ui := t.Status.UserInput; ui != nil && t.Status.Phase == model.PhaseWaitingForUserInput {
		fmt.Printf("  asking   %s\n", ui.Prompt)
		fmt.Printf("  reqId    %s\n", ui.RequestID)
		fmt.Printf("\n  weave send --dir <dir> --request-id %s --input \"...\"\n", ui.RequestID)
	}
	if len(t.Status.Outputs) > 0 {
		fmt.Printf("  outputs  (from attempt %d)\n", t.Status.OutputsAttempt)
		names := make([]string, 0, len(t.Status.Outputs))
		for k := range t.Status.Outputs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Printf("             %-10s %s\n", k, truncate(t.Status.Outputs[k], 60))
		}
	}
	if t.Status.FailureReason != "" {
		fmt.Printf("  failure  %s — %s\n", t.Status.FailureReason, t.Status.FailureMessage)
	}
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func die(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "weave: "+format+"\n", args...)
	return 1
}
