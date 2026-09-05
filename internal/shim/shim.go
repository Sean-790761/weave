// Package shim is the container entrypoint wrapper.
//
//	command: ["weave", "shim", "--", "python", "agent.py"]
//
// It exists so that the agent never has to know about exit code 77, the
// termination log, request IDs, or envelope versioning. An agent that simply
// writes /workspace/output.json and exits 0 works with no SDK at all.
//
// It also guarantees an envelope on every exit path, including SIGSEGV and
// OOMKill, so a crashed agent never leaves an unattributable empty status.
package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Sean-790761/weave/internal/envelope"
)

type Config struct {
	Workspace      string // WEAVE_WORKSPACE, default /workspace
	TerminationLog string // WEAVE_TERMINATION_LOG, default /dev/termination-log
	Attempt        int    // WEAVE_ATTEMPT, default 1
}

func ConfigFromEnv() Config {
	c := Config{
		Workspace:      envOr("WEAVE_WORKSPACE", "/workspace"),
		TerminationLog: envOr("WEAVE_TERMINATION_LOG", "/dev/termination-log"),
		Attempt:        1,
	}
	if v := os.Getenv("WEAVE_ATTEMPT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Attempt = n
		}
	}
	return c
}

func (c Config) outputPath() string { return filepath.Join(c.Workspace, "output.json") }
func (c Config) askPath() string    { return filepath.Join(c.Workspace, ".weave", "ask.json") }
func (c Config) failPath() string   { return filepath.Join(c.Workspace, ".weave", "fail.json") }

// Run executes argv and returns the exit code to propagate.
func Run(c Config, argv []string) int {
	if len(argv) == 0 {
		writeLog(c, &envelope.Envelope{
			V: envelope.Version, Kind: envelope.KindFailed, Attempt: c.Attempt,
			Reason: envelope.ReasonContractViolation, Message: "shim invoked with no command",
		})
		return 1
	}

	// Clear per-attempt scratch so a retry can never read the previous
	// attempt's output. Durable agent state under .weave/ is left alone.
	_ = os.MkdirAll(filepath.Join(c.Workspace, ".weave"), 0o755)
	for _, p := range []string{c.outputPath(), c.askPath(), c.failPath()} {
		_ = os.Remove(p)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Dir = c.Workspace
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		writeLog(c, &envelope.Envelope{
			V: envelope.Version, Kind: envelope.KindFailed, Attempt: c.Attempt,
			Reason: envelope.ReasonError, Message: "exec: " + err.Error(),
		})
		return 127
	}

	// Forward termination signals so `weave cancel` and node drains give the
	// agent a chance to checkpoint.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()

	err := cmd.Wait()
	signal.Stop(sigs)
	close(sigs)

	code := exitCode(err)
	env := c.build(code)
	writeLog(c, env)
	return code
}

func (c Config) build(code int) *envelope.Envelope {
	base := envelope.Envelope{V: envelope.Version, Attempt: c.Attempt}

	switch code {
	case envelope.ExitAsk:
		var ask struct {
			Prompt    string `json:"prompt"`
			RequestID string `json:"requestId"`
		}
		if err := readJSON(c.askPath(), &ask); err != nil {
			base.Kind = envelope.KindFailed
			base.Reason = envelope.ReasonContractViolation
			base.Message = fmt.Sprintf("exit %d without %s: %v",
				envelope.ExitAsk, c.askPath(), err)
			return &base
		}
		if ask.RequestID == "" {
			ask.RequestID = newRequestID()
		}
		base.Kind = envelope.KindWaiting
		base.Prompt = ask.Prompt
		base.RequestID = ask.RequestID
		return &base

	case 0:
		outs, err := readOutputs(c.outputPath())
		if err != nil {
			base.Kind = envelope.KindFailed
			base.Reason = envelope.ReasonContractViolation
			base.Message = "reading output.json: " + err.Error()
			return &base
		}
		base.Kind = envelope.KindDone
		base.Outputs = outs
		return &base

	default:
		// Let the agent classify its own failure if it bothered to.
		var f struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		}
		base.Kind = envelope.KindFailed
		base.Reason = envelope.ReasonError
		base.Message = fmt.Sprintf("agent exited with code %d", code)
		if err := readJSON(c.failPath(), &f); err == nil && f.Reason != "" {
			base.Reason = f.Reason
			if f.Message != "" {
				base.Message = f.Message
			}
		}
		return &base
	}
}

// readOutputs flattens output.json into map[string]string: JSON strings are
// unquoted, everything else keeps its compact JSON text. A missing file is not
// an error here — required-ness is declared in the topology, so only the
// controller can judge it.
func readOutputs(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("output.json must be a JSON object: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	return out, nil
}

func writeLog(c Config, e *envelope.Envelope) {
	b := e.Marshal()
	if err := os.WriteFile(c.TerminationLog, b, 0o644); err != nil {
		// Best effort: surface on stderr so at least the log plane has it.
		fmt.Fprintf(os.Stderr, "weave-shim: cannot write %s: %v\nweave-shim: %s\n",
			c.TerminationLog, err, b)
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 128 + int(st.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
