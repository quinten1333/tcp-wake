package proxy

import (
	"context"
	"errors"
	"os/exec"
	"strconv"

	"tcp-wake/internal/config"
)

// WakeResult is the outcome of one wake-command execution. ExitCode is 0 for a
// clean exit, the process's code for a non-zero exit, and -1 when the command
// could not be launched at all. Err is nil only for a clean exit.
type WakeResult struct {
	Command  string
	ExitCode int
	Err      error
}

// Status renders the exit status for the log line (IF-6). A launch failure has
// no exit code, so it is named rather than numbered.
func (r WakeResult) Status() string {
	if r.ExitCode < 0 {
		return "launch-failed"
	}
	return strconv.Itoa(r.ExitCode)
}

// WakeTrigger executes the configured wake command once for each request
// received while the target is not healthy (FR-3, IF-4). The command is a
// single path run directly as a child process: no shell, no argument splitting.
// The elevated privilege is a property of the setuid-root file itself (ADR-0005,
// NFR-7), so the trigger neither needs nor grants privileges of its own.
type WakeTrigger struct {
	command string
}

// NewWakeTrigger returns a trigger for cfg.WakeCommand.
func NewWakeTrigger(cfg *config.Config) *WakeTrigger {
	return &WakeTrigger{command: cfg.WakeCommand}
}

// Command returns the configured command path, which the request path reports
// to the client on failure (FR-17).
func (w *WakeTrigger) Command() string {
	return w.command
}

// Run executes the command once and reports its exit status. The command
// inherits the process environment; its stdout and stderr are discarded so only
// the one ADR-0010 line describes the execution. A non-zero exit and a failure
// to launch are both reported as an error and both produce the FR-17 response.
func (w *WakeTrigger) Run(ctx context.Context) WakeResult {
	cmd := exec.CommandContext(ctx, w.command)
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Run()
	res := WakeResult{Command: w.command, ExitCode: 0, Err: nil}
	if err == nil {
		return res
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		res.Err = err
		return res
	}

	res.ExitCode = -1
	res.Err = err
	return res
}
