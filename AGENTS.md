# Agent memory
This file is your memory. Update this file as the project progresses. You can create new sections as you see fit.

# Project architecture
tcp-wake is a wake-on-demand reverse proxy in Go deployed as a container on
hyperion in front of hypha (normally powered off). It holds hypha-bound requests
with no deadline, execs a setuid-root wake command, polls hypha's `/health`, and
forwards held requests verbatim once healthy. Source of truth: `docs/architecture.md`
(arc42, 15 ADRs accepted) plus `docs/specs/SRS.md` and `docs/implementation-plan.md`.
Build order: T1 (blocking prerequisite) then T2–T16. See `docs/implementation-plan.md`.

# Learnings
- **T1 / setuid in containers.** The setuid effect depends on the mount options,
  not just the file mode. `/tmp` on this host is a `nosuid` tmpfs, and a Docker
  bind mount inherits the source filesystem's options, so a stub built under
  `/tmp` shows mode `4755` but executes with no privilege. Build the stub in the
  repository checkout (btrfs root subvolume, no `nosuid`). The definitive check is
  to **execute** the binary and read `geteuid()`, not to `stat` it. Verified PASS
  inside `debian:stable-slim` as uid 1000 (`effective_uid=0`).
- Use a glibc-based image (`debian:stable-slim`) to run glibc-compiled stubs;
  Alpine's musl gives a misleading `not found` on a glibc dynamic binary.
- **Environment.** Arch Linux VM; `arch` user is in the `docker` group; Docker
  29.8.1 with daemon active. `sudo` is available for root-owned artifacts.
- **T2 / config loader.** `internal/config.Load(args, lookupEnv)` resolves the
  file `--config` -> `$TCPWAKE_CONFIG` -> `/etc/tcp-wake/config.toml` (no CWD
  fallback, ADR-0015), overlays `TCPWAKE_<KEY>` env vars (env wins, ADR-0011),
  then parses/validates. Precedence is a merge over raw strings: defaults ->
  file -> env -> parse. `rawConfig` defaults are pinned to `docs/config.example.toml`
  by a test; helpers are dependency-injected so tests never touch the real
  environment or `/etc`. A set-but-empty env override is an error, not a silent
  fallback; unknown TOML keys fail start. `cmd/tcp-wake/main.go` loads config and
  exits non-zero on failure.
- **T2 / TOML merge mechanics.** `toml.Decode` into a *pre-filled* struct only
  overwrites keys present in the file, so "defaults, then file, then env" falls
  out of decode order — keep `rawConfig` prefilled, don't zero it first. Use the
  returned `toml.MetaData.Undecoded()` to reject unknown keys. Parse flags with
  `flag.NewFlagSet(name, flag.ContinueOnError)` and `fs.SetOutput(io.Discard)` so
  no global `flag.CommandLine` state leaks and usage noise stays out of errors.
- **T3 / Listener and Intake.** The request path lives in `internal/proxy`.
  Bytes are retained verbatim and never parsed: `Intake` reads header lines one at
  a time with `bufio.Reader.ReadBytes('\n')` so the reader stops exactly at the
  body (nothing read ahead), inspects only `Content-Length`/`Transfer-Encoding`
  for framing, and copies the body unchanged. Chunked bodies count body bytes
  against `held_body_cap` as they arrive, so at most the cap is buffered; an
  over-cap request gets the ADR-0014 413 (`component=held_body_cap`, `limit`) from
  `writeError` in `errors.go`, the seam T8 reuses for the other three errors.
  A framing error closes the connection with no synthetic response, because the
  four JSON bodies are the only responses the system produces (ADR-0009).
- **T3 / detecting a client close without a deadline (NFR-4).** A goroutine blocks
  in `Read` on the same buffered reader until the client closes, then calls
  `Pending.Discard()` (idempotent, `sync.Once`) and removes it from the `HeldSet`.
  No `SetReadDeadline`/`SetWriteDeadline`/`SetDeadline` appears in the package;
  `TestNFR4NoDeadlineCallsInSource` scans the non-test sources to keep it that way,
  and `deadlineSpy`/`spyListener` prove it at runtime. `HeldSet` is mutex-guarded
  and uncapped (ADR-0012); `CloseAll` drains on shutdown.
- **T3 / Intake's signature and cap scope.** `Intake(conn, r *bufio.Reader, limit)`
  takes the reader that the caller (and later the close watcher) shares; calling
  `bufio.NewReader(conn)` again would discard bytes already buffered and break
  byte-exactness. The cap is measured on **body** bytes only (FR-20 and the
  `held_body_cap` comment), not the whole request. `rejectTooLarge` writes the 413
  and closes the connection inside Intake; the listener's later `conn.Close()` is a
  harmless double close. `Serve`'s context goroutine needs its own stop channel, or
  it outlives `Serve` when Accept fails for a non-context reason.
- **T3 / test wiring.** `Listener.listen` is injectable, so tests bind
  `127.0.0.1:0` and learn the port. Tests are white-box (`package proxy`). When a
  net.Pipe test reads the response, the client write must run in a goroutine and a
  read deadline must guard the test, or a regression that stops rejecting an
  over-cap request hangs the suite until the 10-minute go-test timeout.

- **T4 / Health state and Probe.** `internal/proxy/health.go` holds both the
  belief (`Health`) and the cadence runner (`Prober`); `probe.go` holds the HTTP
  observation. `Health.observe(ready)` is **unexported on purpose**: a probe
  result is the only input the state accepts, so ADR-0008's "never infer from a
  failure's cause" is structural — no exported setter exists for the forwarder to
  misuse. `WaitHealthyOr(ctx, done)` blocks until healthy, `done` (the request's
  `Discarded()`), or ctx; it uses a close-and-replace broadcast channel so a
  waiter cannot miss a transition. A new process starts not healthy (FR-16).
- **T4 / probe gating (FR-12, ADR-0008).** `Prober.RequestStarted` starts the
  loop only on the 0->1 pending edge and only while `!health.Healthy()`;
  `RequestDone` stops it at pending 0. The loop probes **immediately**, then every
  `probe_interval`, and exits on the **first ready** observation, so a healthy
  target is never probed on a timer. `ProbeNow` runs one probe and sets the state
  from the result — the FR-19 seam T7 calls after a 502; it does not start the
  loop. `pending`/`running`/`stop` are all guarded by `Prober.mu`, and
  `stopLocked` clears `running` before returning so a `RequestDone`/`Close` race
  closes the stop channel exactly once.
- **T4 / HTTP probe details.** Ready is HTTP 200 **and** body
  `{"status":"ok"}` (IF-3); other status, other body, timeout, and transport error
  are all not healthy (FR-11). `httpProbe` uses `context.WithTimeout` for
  `probe_timeout`, `DisableKeepAlives` (the target may be mid-boot), a 256 B
  `io.LimitReader` on the body, and always closes it. The URL is
  `strings.TrimRight(target, "/") + path`. The probe function is a struct field
  (`probeFn`) so white-box tests replace it with a stub for cadence/gating tests.
- **T4 / test gotcha.** An httptest server whose handler blocks forever will hang
  `srv.Close()` (it waits for the abandoned request) until the 10-minute go-test
  timeout. Release the handler **before** closing the server, e.g.
  `defer func() { close(release); srv.Close() }()`.
- **T5 / Wake trigger.** `internal/proxy/wake.go` holds `WakeTrigger`: it
  execs `cfg.WakeCommand` **directly** as one path — no `sh -c`, no whitespace
  splitting, no args — because ADR-0005 makes the elevated privilege a property
  of the setuid-root file, and setuid scripts do not work. The command inherits
  the process env but its stdout/stderr are set to `nil` (discarded) so only the
  one ADR-0010 line describes the execution. `WakeResult{Command, ExitCode, Err}`
  distinguishes a clean exit (0, nil err), a non-zero exit (`*exec.ExitError`
  with its code), and a launch failure (`ExitCode == -1`, `status=launch-failed`);
  both failure shapes feed the FR-17 500. `TestWakeExecIsNotShellSplit` pins the
  no-shell rule by exec'ing a path that contains a space.
- **T5 / Logger boundary with T9.** `internal/proxy/log.go` has a deliberately
  narrow `Logger` with only `Wake(command, res)`; T9 adds the error line and the
  "no request/response content" inspection to the same type rather than
  reimplementing the wake half. The mutex serialises whole lines under
  concurrent executions. The line is RFC3339 + `wake command="…" status=…`.
- **T5 / Pipeline is the request-path seam.** The 500 must be written with the
  package-private `writeError`, so the path that wakes, writes the 500, starts
  the probe, and holds lives in `internal/proxy/pipeline.go` as
  `Pipeline.Handle`; `cmd/tcp-wake` only wires and passes `pl.Handle`. T6 (wait
  bound) and T7 (forwarder) extend `Handle`, they do not replace it. A wake
  failure returns before `prober.RequestStarted`, so a failed request never
  holds.
- **T5 / test technique and gotcha.** Stub wake commands are `#!/bin/sh` scripts
  in `t.TempDir()` (`recordingCommand` appends one line per execution to a
  record file; `failingCommand` exits non-zero); no setuid is needed for tests,
  that is the T14/T15 deployment check. A `-race` run trips if the test reads a
  `bytes.Buffer` the logger is concurrently writing, so shared log capture uses
  the mutex-guarded `syncBuffer` in `helpers_test.go`. Do not use `pkill -f
  tcp-wake` patterns in a shell whose own command line contains that string —
  `pkill -f` matches the shell itself and kills it mid-command.
- **Linting.** There is no `golangci-lint`/config; `gofmt -l .` and `go vet ./...`
  are the linters. Run `go test -count=1 ./...` (and optionally `-race`) when a
  cached PASS could mask a change.

# Workflow and process
- **Commit format:** the user wants `[<n>] <Summarized task title> <Summary of Change>`,
  and a push after each milestone.
- **Plan tracking:** tick each task's checkbox in `docs/implementation-plan.md` as it
  lands, and append a dated entry to `docs/log.md`.
- **Traceability check** (the only check that exists so far):
  `python3 scripts/check_traceability.py docs/specs/SRS.md docs/architecture.md`
  — note the spec is at `docs/specs/SRS.md`, not `specs/SRS.md`. It must exit 0.
- **Go module (T2 landed).** Module path `tcp-wake`, Go 1.27.1. Checks before a
  commit: `gofmt -l .` (must print nothing), `go build ./...`, `go vet ./...`,
  `go test ./...`. The only third-party dependency is `github.com/BurntSushi/toml`
  (ADR-0013 permits one small dependency); `go mod tidy` keeps it pinned.
- **Manual smoke test (hold path).** With a TOML holding only
  `listen_address = "127.0.0.1:18080"`, run `TCPWAKE_CONFIG=/path/tcpwake.toml ./tcp-wake`;
  a raw `GET` sent with bash `/dev/tcp` must receive zero response bytes (the process
  holds it), proving FR-2/FR-6 without a fake hypha.
- **Manual smoke test (probe path, T4).** Point `target_address` at a throwaway
  local HTTP server that logs each `GET` and answers 503, set a short
  `probe_interval`, and start the binary. While nothing is connected the log must
  stay empty (FR-12); hold one request via bash `/dev/tcp` and probes must begin
  arriving at `health_path` while the client still receives zero bytes. Flip the
  server to `200 {"status":"ok"}` and the held connection must release.
- **Don't commit build artifacts.** T1's `stub`/`stub.c` are gitignored.
