package proxy

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Logger writes the system's text log lines to a writer: one per wake-command
// execution (IF-6) and one per error (ADR-0010), as greppable text. No request
// or response content is ever written (§2 non-goal 3), which is the point of
// logging only wake executions and errors. The log lines are the counting
// artefact for FR-3, FR-9, and FR-12.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

// NewLogger returns a Logger writing to out (os.Stdout in production).
func NewLogger(out io.Writer) *Logger {
	return &Logger{out: out}
}

// Ready reports whether the log's writer accepts writes. The log is the
// counting artefact for FR-3, FR-9, and FR-12, so an unwritable log is a
// start-time failure (SRS §5.6). The probe writes zero bytes, so it cannot
// pollute that count. It detects the production case — a closed or otherwise
// invalid standard output — but a writer that only fails on non-empty writes
// is not caught; that is an accepted limitation.
func (l *Logger) Ready() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.out.Write(nil); err != nil {
		return fmt.Errorf("proxy: log output is not writable: %w", err)
	}
	return nil
}

// Wake writes one line for one wake-command execution, carrying a timestamp,
// the command, and its exit status (§5.6). It writes exactly one line per call;
// the count of these lines is the counting artifact for FR-3, FR-9, and FR-12.
// The mutex keeps concurrent executions from interleaving a line.
func (l *Logger) Wake(command string, res WakeResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s wake command=%q status=%s\n",
		time.Now().UTC().Format(time.RFC3339), command, res.Status())
}

// Error writes one line for one error, carrying a timestamp, the HTTP status
// the client was given, the enumerable ADR-0014 component, and the same message
// the client's body carries. It writes exactly one line per call, under the
// same mutex as Wake. The message is built only from configuration and
// transport errors, never from request or response bytes (ADR-0010, §2
// non-goal 3). Framing errors are deliberately not logged: ADR-0010's "error"
// is the four response-producing paths.
func (l *Logger) Error(status int, detail errorDetail) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s error status=%d component=%s message=%q\n",
		time.Now().UTC().Format(time.RFC3339), status, detail.Component, detail.Message)
}

// Health writes one line for one target health-state transition (ADR-0017). It
// carries a timestamp and the new state, is greppable, and writes no request or
// response content. It is called only when the belief actually changes, so the
// count of these lines is the number of transitions, not the number of probes.
func (l *Logger) Health(healthy bool) {
	state := "unhealthy"
	if healthy {
		state = "healthy"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s health state=%s\n", time.Now().UTC().Format(time.RFC3339), state)
}
