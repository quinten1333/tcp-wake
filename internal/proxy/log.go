package proxy

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Logger writes the system's text log lines to a writer, one per wake-command
// execution (IF-6, ADR-0010). It is deliberately narrow: T9 adds the error line
// on top of the same type. No request or response content is ever written
// (§2 non-goal 3), which is the point of logging only wake executions and
// errors.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

// NewLogger returns a Logger writing to out (os.Stdout in production).
func NewLogger(out io.Writer) *Logger {
	return &Logger{out: out}
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
