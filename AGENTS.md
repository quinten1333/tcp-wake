# Agent memory
This file is your memory. Update this file as the project progresses. You can create new sections as you see fit.

# Project architecture
tcp-wake is a wake-on-demand reverse proxy in Go deployed as a container on
hyperion in front of hypha (normally powered off). It holds hypha-bound requests
with no deadline, execs `etherwake` (image-installed with a `CAP_NET_RAW` file
capability), polls hypha's `/health`, and forwards held requests verbatim once
healthy. Source of truth: `docs/architecture.md` (arc42, 16 ADRs: 15 accepted,
ADR-0005 superseded by ADR-0016) plus `docs/specs/SRS.md` v0.2 and
`docs/implementation-plan.md`. Build order: T1 (blocking prerequisite) then
T2–T16; ADR-0016 later replaced the wake mechanism. See
`docs/implementation-plan.md`.

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
  `Pending.Discard()` (idempotent, `sync.Once`) and removes it from the unexported `heldSet`.
  No `SetReadDeadline`/`SetWriteDeadline`/`SetDeadline` appears in the package;
  `TestNFR4NoDeadlineCallsInSource` scans the non-test sources to keep it that way,
  and `deadlineSpy`/`spyListener` prove it at runtime. `heldSet` is mutex-guarded
  and uncapped (ADR-0012); `closeAll` drains on shutdown.
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
- **T5 / Wake trigger (updated by ADR-0016).** `internal/proxy/wake.go` holds
  `WakeTrigger`, which execs the **fixed** `/usr/sbin/etherwake` with an explicit
  argv (`-i <wake_interface> <wake_mac>`) — no `sh -c` and no whitespace
  splitting. ADR-0016 replaced the old configurable `wake_command`: the image
  installs etherwake and sets `cap_net_raw+ep` on it, so the wake tool gets only
  the raw-socket capability. `WakeTrigger.command`/`args` are fields so tests
  substitute a stub; `Command()` renders `path + args` for the FR-17 body and
  the log. The command inherits the process env but its stdout/stderr are `nil`
  (discarded) so only the one ADR-0010 line describes the execution.
  `WakeResult{ExitCode, Err}` distinguishes a clean exit (0, nil err), a
  non-zero exit (`*exec.ExitError` with its code), and a launch failure
  (`ExitCode == -1`, `status=launch-failed`); both failure shapes feed the FR-17
  500. `TestWakeExecIsNotShellSplit` pins the no-shell rule by exec'ing a path
  that contains a space; `TestNFR7TriggerExecsEtherwake` pins the fixed argv.
- **ADR-0016 / wake configuration and privilege.** `wake_mac` is required and
  `wake_interface` defaults to `eth0`; there is no command key. The privilege is
  the `cap_net_raw+ep` xattr on `/usr/sbin/etherwake`, verified by
  `scripts/check-deploy.sh` with `getcap` plus an execution as uid 10001 (with
  the cap: rc 0; without it: `must be run as root`, rc 2). `chown` clears setuid
  — irrelevant now, but the analogous trap is `no-new-privileges`, which makes a
  file capability inert. Dockerfile builds need `--network=host` in this VM
  because the Docker bridge/veth is unavailable.
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
  the mutex-guarded `syncBuffer` in `helpers_test.go`. When counting executions,
  wait on the **log line**, not the record file: the child appends the record
  before `Run` returns, while `logger.Wake` runs after, so the record can reach
  the expected count a moment before the line. Do not use `pkill -f tcp-wake`
  patterns in a shell whose own command line contains that string — `pkill -f`
  matches the shell itself and kills it mid-command.
- **T6 / Wait-bound timer.** `Pipeline.Handle` wraps the health wait in
  `context.WithDeadline(pl.ctx, p.Arrival.Add(pl.waitBound))`, so the bound is
  anchored to arrival and computed once — a wake attempt cannot restart it
  (FR-8). No goroutine or timer type is added; `WaitHealthyOr` already selects
  on the context and `defer cancel()` releases the timer. The `false` return is
  disambiguated **in order**: a closed `p.Discarded()` is a client departure
  (write nothing, FR-7), `pl.ctx.Err() != nil` is shutdown (write nothing,
  FR-16), and only `errors.Is(waitCtx.Err(), context.DeadlineExceeded)` writes
  the 504. `WaitHealthyOr` reads the belief before selecting, so a healthy
  target at the boundary forwards rather than 504s. `NewPipeline` takes the
  `waitBound` value, not the config. The 504 `limit` is `waitBound.String()`
  (`2m0s` for the default 120s), consistent with the 413 using
  `ByteSize.String()`; T8 owns exact-body assertions.
- **T6 / timing tests.** `waitbound_test.go` drives `Handle` directly over a
  `net.Pipe` `Pending` (`newPipePending`). net.Pipe is synchronous, so the
  client reader must run in a goroutine before `Handle` writes or the write
  blocks: `startHandle` starts the reader and the handler, `wait` blocks on the
  handler, and `response` closes the server conn so the reader sees EOF. Use
  bounds of 100–650 ms with generous upper assertions so `-race` runs are not
  flaky.
- **T7 / Forwarder (transparent relay).** `internal/proxy/forward.go` dials
  the target with bare `net.Dial` (no timeout — §8.5/§2 non-goal 7) and writes
  `p.Bytes` verbatim; it never parses or re-serialises the request. The response
  is relayed with its framing tracked only to know where it ends: chunked via
  the shared `walkChunks` (each segment written to the client as it arrives,
  FR-15), `Content-Length` via `io.CopyN`, otherwise `io.Copy` until close. The
  head is framed **before** it is written, and `Forward` returns an error only
  before any client byte is written; after that, upstream failures are swallowed
  so there is exactly one response (NFR-6). A returned error makes the pipeline
  write the 502 naming `target` and call `ProbeNow` (FR-18/FR-19).
- **T7 / no timeouts.** Nothing on the forward path sets a deadline or uses
  `DialTimeout`; `TestForwardNoTimeoutInSource` scans `forward.go` for
  `DialTimeout`/`Set*Deadline`. Accepted limitation: a `HEAD` response's
  `Content-Length` with no body would make the relay wait, because the method is
  never parsed — HEAD is outside the client profile.
- **T7 / shared chunk walker and head framing.** `walkChunks(r, onSize, emit)` in
  `intake.go` is shared by Intake (cap-enforcing, buffering) and the response
  relay (uncapped, streaming), and the relay reuses `readHead` and `framing` for
  the response head — `framing` already skips line 0, so it reads a request or a
  response head identically. Both `ErrFraming`'s text and `readHead`'s messages
  are request-worded but reused for responses; if you change chunk or head
  framing, both paths change.
- **T7 / streaming test gotchas.** `TestFR15StreamsChunksUnbuffered` uses a
  per-chunk handshake: the target writes a chunk, waits for the client's ack,
  then writes the next, so a buffering regression deadlocks. The client must
  also drain the terminating `0` chunk **and** the trailer blank line, or the
  relay blocks writing the trailer and the test hangs. net.Pipe is synchronous,
  so the reader runs in a goroutine.
- **T8 / error responses and the NFR-6 invariant.** `errors.go` is the single
  seam: `writeError` is the only place the system writes a response of its own,
  and the four ADR-0014 component tokens (`wait_bound`, `wake_command`,
  `target`, `held_body_cap`) are constants so code and tests cannot drift. A
  source-scan test (`TestNFR6OnlyErrorsDotGoWritesAResponse`) keeps a future
  second write site from bypassing the "exactly one response" property. The 504
  `limit` is the Go canonical duration (`2m0s` for a 120s config), not the TOML
  spelling, because the parsed config does not retain the original text; the
  test asserts `time.ParseDuration(limit) == configured` rather than a literal.
  `TestADR0014ErrorBodySchema` triggers each of the four real paths over a
  `responseCounter` (a `net.Conn` that records every byte the proxy writes) and
  `parseErrorResponse` asserts the shared framing; `TestNFR6...` counts one
  response per connected request and zero for an abandoned one.
- **T8 / counting responses.** Do not count responses by substring — a relayed
  body can contain `HTTP/1.1`. Replay the recorded bytes through
  `http.ReadResponse`; note that a second `ReadResponse` at EOF returns
  `io.ErrUnexpectedEOF`, so guard with `r.Peek(1) == io.EOF` before each read.
  net.Pipe is synchronous, so the client end must be drained in a goroutine or
  every proxy write blocks.
- **T8 / shared error-test helpers.** New error-path tests should reuse, not
  re-implement: `parseErrorResponse(t, resp)` (in `intake_test.go`) asserts the
  shared ADR-0009 framing and returns `(status, errorDetail)`; `countingPending`
  (in `errors_test.go`) gives a `Pending` whose writes are recorded by a
  `responseCounter`, and `oneResponse` asserts exactly one was written.
  `TestADR0014ErrorBodySchema` is the place to add a fifth condition if the set
  ever grows; the FR-8/17/18 tests now only assert status + component on top of
  the shared parser.
- **T9 / the two log lines.** `internal/proxy/log.go` has `Wake(command,
  res)` → `<RFC3339> wake command="…" status=…` and `Error(status, detail)` →
  `<RFC3339> error status=<code> component=<token> message="…"`, both under one
  mutex so lines never interleave. The error line reuses the ADR-0014
  `errorDetail`, and `Pipeline.writeAndLog` passes the same value to `writeError`
  and `Logger.Error`, so the body and the log cannot drift. Only the four
  response-producing paths are logged; a framing error is not, because it has no
  response (ADR-0010). The 413 is logged by the `Listener` (it is produced
  outside the pipeline), which is why `NewListener(cfg, handle, logger)` now
  takes a logger. `Logger.Ready()` is the SRS §5.6 start-time check: it writes
  zero bytes (`Write(nil)`) so it cannot pollute the wake-line count, and catches
  a closed stdout; `main` exits non-zero if it fails.
- **T9 / no-content rule.** Guarded two ways: a source scan
  (`TestLoggerNeverTakesContent`) asserts no `Logger` method takes `[]byte`,
  `*Pending`, or `net.Conn`; and `TestLoggerNoRequestOrResponseContent` drives
  bodies carrying sentinel tokens through the 502 and relay paths and asserts the
  tokens are absent from the log. When adding a log line, keep messages built
  only from configuration and transport errors.
- **T9 / held-request test race (pre-existing T7 flake).** Tests that hold
  requests while not healthy and then call `health.observe(true)` by hand are
  racy: a `notReadyProber` stub's next probe calls `observe(false)` and undoes
  the manual observation, so the waiters never release and the read times out.
  Use `bootingProber(t, health, interval)`, which reports not-ready until the
  returned `boot()` is called and then observes ready, so the cadence loop and
  the test cannot race. Added in `health_test.go`.
- **T9 / log-line count assertions.** A failing wake now writes **two** lines
  (wake + error 500); tests must count by form (`wake command=`,
  `" error status="`), not by total `\n`, or they will break on the error line.
- **T11 / integration harness.** `internal/proxy/harness_test.go` wires the
  real path around a controllable `fakeTarget`, the recording wake command, and
  a fake client. `system` (newSystem/startSystem/restart/send/sendRaw/hold/
  waitHeld) is the shared scaffold for T10 and T12; prefer extending it over
  adding one-off helpers. The fake target reads each request with production
  `Intake`, so a recorded request is byte-for-byte the forwarded one. Its
  `requestPath` parses with `strings.Fields` on the request line — cutting once
  on a space leaves the HTTP version attached and misclassifies every probe.
  The target starts not ready; a test flips it with `setReady`, so the state
  change still comes from a real probe (ADR-0008) and cannot race the loop.

- **SOLID review (post-T16).** File layout after the review: `health.go`
  (`Health` belief) and `prober.go` (`Prober` cadence); `intake.go` (Intake +
  the two error vars + `maxHeadBytes` + `rejectTooLarge`) and `framing.go`
  (shared `readHead`/`framing`/`walkChunks`/trailers used by both Intake and the
  response relay); `internal/config/config.go` (loader) and
  `internal/config/bytesize.go` (`ByteSize`, `ParseByteSize`, `String`). `writeError` returns
  nothing (marshalling strings cannot fail; a write failure is the client
  leaving). `Health.WaitHealthy` was removed — use `WaitHealthyOr(ctx, nil)`.
  Test-side source scanners share `scanNonTestSources` in `helpers_test.go`.

- **Naming and exposure (post-T5 review).** The Listener constructor is
  `NewListener`, not `New`, because the package has several constructors and
  `New` was ambiguous. The held set is unexported (`heldSet`/`newHeldSet`,
  `add`/`remove`/`count`/`closeAll`) since it is a Listener detail; tests observe
  it through `Listener.heldCount()`. `Handler` returns nothing — the pipeline
  writes its own responses and logs its own errors, so a returned error was dead
  surface. `WakeResult` no longer carries the command (the trigger and logger
  hold it). `Pipeline` stores only the `waitBound` value plus a `*Forwarder`, not
  the whole `*config.Config`; keep injecting the specific dependency a new block
  needs (T6 added `waitBound`, T7 the forwarder) rather than the config.
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
- **Manual smoke test (wake path, ADR-0016).** The wake command is fixed to
  etherwake, so the smoke test for the FR-17 failure path uses an invalid
  `wake_interface` (etherwake exits non-zero) rather than swapping a command.
  For the success path, point `target_address` at a local server answering 503
  and set `wake_mac` to any MAC; hold requests via bash `/dev/tcp` and stdout
  gets one `wake command="/usr/sbin/etherwake -i … …" status=0` line each. Do
  not `wait` on the proxy/target background jobs — they run until killed; wait
  only on the client PIDs.
- **Don't commit build artifacts.** T1's `stub`/`stub.c` are gitignored.
