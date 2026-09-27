package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubCommand writes an executable shell script and returns its path. Tests
// point wake_command at it; the setuid bit is a deployment concern (T1, T14,
// T15), not something a test needs.
func stubCommand(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cmd.sh")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// recordingCommand returns a command that appends one line to a record file per
// execution, so tests can count executions.
func recordingCommand(t *testing.T) (path, record string) {
	t.Helper()
	record = filepath.Join(t.TempDir(), "record")
	path = stubCommand(t, "#!/bin/sh\necho invoked >> '"+record+"'\n")
	return path, record
}

// failingCommand returns a command that exits with code.
func failingCommand(t *testing.T, code int) string {
	t.Helper()
	return stubCommand(t, fmt.Sprintf("#!/bin/sh\nexit %d\n", code))
}

// countLines counts non-empty lines in a file, reporting 0 if it does not exist.
func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// TestIF4WakeExecutesChildAndReadsExitStatus covers IF-4: the configured
// command runs as a child process and its exit status is read.
func TestIF4WakeExecutesChildAndReadsExitStatus(t *testing.T) {
	path, record := recordingCommand(t)
	tr := &WakeTrigger{command: path}

	res := tr.Run(context.Background())
	if res.Err != nil {
		t.Fatalf("Run returned %v, want a clean exit", res.Err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := countLines(record); got != 1 {
		t.Fatalf("command ran %d time(s), want 1 (IF-4)", got)
	}
}

// TestFR17NonZeroExitIsReported covers FR-17's trigger: a non-zero exit is
// observable and carries the command's code.
func TestFR17NonZeroExitIsReported(t *testing.T) {
	tr := &WakeTrigger{command: failingCommand(t, 3)}

	res := tr.Run(context.Background())
	if res.Err == nil {
		t.Fatal("Run reported success for a non-zero exit")
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

// TestFR17LaunchFailureIsReported covers FR-17 for a command that cannot be
// launched: a missing file is a failure just as a non-zero exit is.
func TestFR17LaunchFailureIsReported(t *testing.T) {
	tr := &WakeTrigger{command: filepath.Join(t.TempDir(), "does-not-exist")}

	res := tr.Run(context.Background())
	if res.Err == nil {
		t.Fatal("Run reported success for a missing command")
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1 for a launch failure", res.ExitCode)
	}
	if res.Status() != "launch-failed" {
		t.Fatalf("Status() = %q, want launch-failed", res.Status())
	}
}

// TestWakeExecIsNotShellSplit pins the direct-exec decision: a path containing a
// space is one path, not a command and its argument. If the trigger split it or
// went through a shell, the real file at the spaced path would not run.
func TestWakeExecIsNotShellSplit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "my wake")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := &WakeTrigger{command: p}
	if res := tr.Run(context.Background()); res.Err != nil {
		t.Fatalf("path with a space was not exec'd directly: %v", res.Err)
	}
}

// TestNFR7TriggerExecsConfiguredFile covers the trigger's half of NFR-7: it
// invokes exactly the configured file as a child process. The privilege check
// itself is the deployment inspection owned by T14/T15.
func TestNFR7TriggerExecsConfiguredFile(t *testing.T) {
	path, record := recordingCommand(t)
	tr := &WakeTrigger{command: path}

	if tr.Command() != path {
		t.Fatalf("Command() = %q, want the configured %q", tr.Command(), path)
	}
	if res := tr.Run(context.Background()); res.Err != nil {
		t.Fatalf("Run returned %v", res.Err)
	}
	if got := countLines(record); got != 1 {
		t.Fatalf("configured file ran %d time(s), want 1", got)
	}
}

// TestIF6OneLogLinePerExecution covers IF-6: one line per wake-command
// execution, each carrying a timestamp and the exit status.
func TestIF6OneLogLinePerExecution(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	logger.Wake("/usr/local/bin/wol-send", WakeResult{ExitCode: 0})
	logger.Wake("/usr/local/bin/wol-send", WakeResult{ExitCode: 3, Err: fmt.Errorf("exit 3")})
	logger.Wake("/usr/local/bin/wol-send", WakeResult{ExitCode: -1, Err: fmt.Errorf("not found")})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d log lines, want 3 (IF-6):\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "wake command=") || !strings.Contains(line, "status=") {
			t.Fatalf("log line missing the command or status: %q", line)
		}
		if _, err := time.Parse(time.RFC3339, strings.Fields(line)[0]); err != nil {
			t.Fatalf("log line does not start with an RFC3339 timestamp: %q", line)
		}
	}
}

// TestFR12NoExecWhileIdle covers FR-12 for the wake path: with no request
// handled, the command never runs.
func TestFR12NoExecWhileIdle(t *testing.T) {
	cmd, record := recordingCommand(t)
	health := NewHealth()
	cfg := testConfig()
	cfg.WakeCommand = cmd
	prober := stubProber(health, time.Hour, func(context.Context) bool { return false })
	pl := NewPipeline(context.Background(), time.Hour, health, prober, NewWakeTrigger(cfg), testForwarder(t, "http://127.0.0.1:1"), NewLogger(io.Discard))
	defer prober.Close()

	// The pipeline exists and the probe stub is live, but no request is
	// handled, so no execution may happen.
	time.Sleep(30 * time.Millisecond)
	if got := countLines(record); got != 0 {
		t.Fatalf("wake command ran %d time(s) with no request, want 0 (FR-12)", got)
	}
	_ = pl.Handle
}

// TestFR3FiveRequestsProduceFiveExecutions covers FR-3: each request received
// while the target is not healthy triggers its own execution.
func TestFR3FiveRequestsProduceFiveExecutions(t *testing.T) {
	cmd, record := recordingCommand(t)
	health := NewHealth()
	cfg := testConfig()
	cfg.WakeCommand = cmd
	prober := stubProber(health, time.Hour, func(context.Context) bool { return false })
	logBuf := &syncBuffer{}
	pl := NewPipeline(context.Background(), time.Hour, health, prober, NewWakeTrigger(cfg), testForwarder(t, "http://127.0.0.1:1"), NewLogger(logBuf))
	defer prober.Close()

	const n = 5
	pendings := make([]*Pending, 0, n)
	for i := 0; i < n; i++ {
		client, server := net.Pipe()
		t.Cleanup(func() {
			client.Close()
			server.Close()
		})
		p := newPending(server, bufio.NewReader(server), []byte("GET / HTTP/1.1\r\n\r\n"))
		pendings = append(pendings, p)
		go pl.Handle(p)
	}

	// Wait on the log line, not the record: the record is written inside the
	// child before Run returns, so the line can lag the record by a moment.
	waitFor(t, "five wake log lines", func() bool { return strings.Count(logBuf.String(), "\n") == n })
	if got := countLines(record); got != n {
		t.Fatalf("wake command ran %d time(s), want %d", got, n)
	}

	for _, p := range pendings {
		p.Discard()
	}
}

// TestFR17ClientGets500NamingWakeCommand covers the FR-17 client path end to
// end: a failed command gives that request's client an immediate 500 whose body
// names the wake command, and one wake line is logged.
func TestFR17ClientGets500NamingWakeCommand(t *testing.T) {
	cmd := failingCommand(t, 3)
	health := NewHealth()
	cfg := testConfig()
	cfg.WakeCommand = cmd
	prober := stubProber(health, time.Hour, func(context.Context) bool { return false })
	defer prober.Close()
	logBuf := &syncBuffer{}
	pl := NewPipeline(context.Background(), time.Hour, health, prober, NewWakeTrigger(cfg), testForwarder(t, "http://127.0.0.1:1"), NewLogger(logBuf))

	_, addr := startListener(t, cfg, pl.Handle)

	conn := get(t, addr)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading the 500 response: %v", err)
	}
	resp := string(data)
	if !strings.HasPrefix(resp, "HTTP/1.1 500") {
		t.Fatalf("response status is not 500:\n%s", resp)
	}
	if !strings.Contains(resp, `"component":"wake_command"`) {
		t.Fatalf("500 body does not name the wake command:\n%s", resp)
	}

	if got := strings.Count(logBuf.String(), "\n"); got != 1 {
		t.Fatalf("wrote %d wake log lines, want 1 (FR-17 verify):\n%s", got, logBuf.String())
	}
}
