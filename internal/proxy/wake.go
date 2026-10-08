package proxy

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"tcp-wake/internal/config"
)

// etherwakePath is the fixed wake tool (ADR-0016). The command is no longer
// configuration: the image installs etherwake and gives it the CAP_NET_RAW file
// capability, and the trigger always invokes it with the configured interface
// and MAC.
const etherwakePath = "/usr/sbin/etherwake"

// WakeResult is the outcome of one wake-command execution. ExitCode is 0 for a
// clean exit, the process's code for a non-zero exit, and -1 when the command
// could not be launched at all. Err is nil only for a clean exit.
type WakeResult struct {
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

// WakeTrigger executes the wake command once for each request received while
// the target is not healthy (FR-3, IF-4). It always runs etherwake as a child
// process with an explicit argv, no shell and no splitting; the elevated
// privilege is the CAP_NET_RAW file capability on the etherwake binary
// (ADR-0016, NFR-7), so the trigger neither needs nor grants privileges of its
// own. The command and args are fields so tests can substitute a stub.
type WakeTrigger struct {
	command string
	args    []string
}

// NewWakeTrigger returns a trigger that runs etherwake on cfg's interface and
// MAC.
func NewWakeTrigger(cfg *config.Config) *WakeTrigger {
	return &WakeTrigger{
		command: etherwakePath,
		args:    []string{"-i", cfg.WakeInterface, cfg.WakeMAC},
	}
}

// Command returns the invocation as one string, which the request path reports
// to the client on failure (FR-17) and the log records (IF-6).
func (w *WakeTrigger) Command() string {
	return strings.Join(append([]string{w.command}, w.args...), " ")
}

// Run executes the command once and reports its exit status. The command
// inherits the process environment; its stdout and stderr are discarded so only
// the one ADR-0010 line describes the execution. A non-zero exit and a failure
// to launch are both reported as an error and both produce the FR-17 response.
func (w *WakeTrigger) Run(ctx context.Context) WakeResult {
	cmd := exec.CommandContext(ctx, w.command, w.args...)
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Run()
	res := WakeResult{}
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
