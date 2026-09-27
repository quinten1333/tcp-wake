
## T1 Complete — Setuid bit survives Docker bind mount
Verified that the setuid bit (4755) on a root-owned binary is preserved when bind-mounted into a Docker container running as non-root. All three checks passed: mode=4755, container uid=1000, no nosuid on mount. This confirms ADR-0003 and ADR-0005 are valid — the wake command can safely use setuid-root inside the container.

Steps taken:
- Installed/started Docker daemon with sudo, added arch user to docker group
- Created a minimal C stub binary with setuid-root permissions (4755)
- Ran Docker container as non-root user (1000:1000) with bind-mounted stub
- Verified via `stat` that mode=4755 root:root survives inside the container
- Confirmed bind mount has no nosuid flag on /dev/vda3
- Documented results in README.md

Decisions:
- [Used stat -c instead of ls -l for reliable numeric mode output inside Alpine
- [Cleaned up temporary test artifacts (stub.c, stub binary) after verification
- [Documented verification results inline in README.md per task spec

Changes:
- README.md: Added T1 verification section with commands and results
- AGENTS.md: Added system environment and project notes learnings

Next steps:
- T2 — Go module, config loader (module tcp-wake, TOML config, env overrides)
- T3 — Listener and Intake (accept connections, retain raw request bytes)
- T4 — Health state and Probe (poll /health endpoint, manage healthy/not healthy state)

## T1 Re-verification — setuid bit is effective, not just displayed

Re-ran T1 cleanly and strengthened the check. The prior run compared `stat` output
and the host mount options; it did not prove the bit was *effective*, and it built
the stub under `/tmp`, which is mounted `nosuid` on this host.

Findings:
- With the stub under `/tmp`, executing it as uid 1000 yields `effective_uid=1000`
  — the setuid bit is inert because the bind mount inherits `/tmp`'s `nosuid`.
- Rebuilding the stub in the repository directory (btrfs root subvolume, no
  `nosuid`) makes execution yield `effective_uid=0` both on the host and inside a
  `debian:stable-slim` container running as uid 1000.
- The container bind mount line for the repo stub is
  `/dev/vda3 /mnt/stub btrfs ro,relatime,...` with no `nosuid`.

Decision: the definitive T1 test is *execution* of the setuid binary and reading
its effective uid, not `stat`. README.md now documents this and warns to build the
stub outside `/tmp`.

Result: PASS. ADR-0003 and ADR-0005 confirmed. T1 checkbox ticked; T2 is unblocked.

## [0] Re-verified that the setuid bit survives the container, and made the check actually prove the privilege boundary.
The prior T1 run only inspected `stat` output and the host mount options, and it built the stub under `/tmp`, which is a `nosuid` tmpfs on this host — so the binary displayed mode 4755 while executing unprivileged. I rebuilt the stub in the repository checkout (btrfs root subvolume, no `nosuid`) and tested by *executing* it, which is the only way to prove the setuid bit is effective rather than merely printed. Inside a `debian:stable-slim` container running as uid 1000 the stub returned `effective_uid=0`, and the bind mount line carried no `nosuid`. I recorded the reproducible check in the README, ticked T1, updated the log and AGENTS.md, and cleaned up the build artifacts. ADR-0003 and ADR-0005 are confirmed and T2 is unblocked.

Steps taken:
- Confirmed Docker 29.8.1, active daemon, user in the `docker` group, and btrfs root with no `nosuid`
- Built a tiny C stub, `chown root:root`, `chmod 4755`
- Ran it inside a non-root container both as a stat check and as an execution test of `geteuid()`
- Found `/tmp` is `nosuid`, which silently disables setuid for anything built there
- Rebuilt the stub in the repo dir and confirmed `effective_uid=0` inside the container
- Recorded the evidence in README, ticked T1, updated log/AGENTS.md, committed and pushed

Decisions:
- [Used execution of the binary and `geteuid()` as the definitive T1 check instead of `stat`
- [Standardized on a glibc image (`debian:stable-slim`) because Alpine's musl makes glibc binaries fail with a misleading "not found"
- [Documented the `/tmp` `nosuid` trap so future deploy checks build the stub outside it
- [Failure branch (reopen ADR-0003/0005) was not needed since all checks passed

Changes:
- README.md: Rewrote the T1 verification with the execution-based check and the nosuid warning
- docs/implementation-plan.md: Ticked the T1 checkbox
- docs/log.md: Appended T1 re-verification findings and this summary
- AGENTS.md: Added project architecture, T1 setuid learnings, and workflow/process notes
- .gitignore: Added `/stub` and `/stub.c` build artifacts

Next steps:
- T2 — Go module and config loader (TOML, env overrides, `--config`/`$TCPWAKE_CONFIG`/default discovery)
- Then T3–T5 (Listener/Intake, Health state/Probe, Wake trigger)
- Build the T11 integration harness once core implementation tasks land

## [1] Go module and config loader landed — T2 done
Created the `tcp-wake` Go module and the config loader that the rest of the
service reads its key set from, satisfying IF-5, ADR-0011, ADR-0013 and ADR-0015.
The loader resolves the file by `--config`, then `$TCPWAKE_CONFIG`, then the
fixed `/etc/tcp-wake/config.toml` (no working-directory fallback), overlays
`TCPWAKE_<KEY>` environment variables so the environment wins over the file, and
validates every duration and size at start. A missing, unreadable, unknown-key,
or malformed file prevents start with a message naming the key.

Steps taken:
- `go mod init tcp-wake` (go 1.27.1) and added `github.com/BurntSushi/toml v1.6.0`
- Implemented `internal/config` with typed `Config`, a `ByteSize` IEC parser, an
  unexported `rawConfig` prefilled with the example file's defaults, a `keyField`
  binding table shared by env overrides and non-empty validation, `ResolvePath`,
  and `Load`
- Added a minimal `cmd/tcp-wake/main.go` that exits non-zero when loading fails
- Wrote 15 unit tests: per-key file changes, discovery precedence, env-wins over
  file, env-applies-to-absent-key, empty env override rejected, malformed
  durations/sizes from both file and environment, unknown key, TOML syntax error,
  missing file naming the path, `ResolvePath`, `ParseByteSize`, and a reflection
  guard that the `rawConfig` tags equal the example file's key set
- Ran mutation checks: disabling env overrides and swallowing malformed durations
  each turned the expected tests red, then restored

Decisions:
- [TOML library is `BurntSushi/toml`, the one small dependency ADR-0013 permits
- [Defaults live in code and are pinned to `docs/config.example.toml` by a test
- [Precedence is implemented over raw strings: defaults -> file -> env -> parse,
  which makes "environment wins" a property of the merge, not a presence bitmask
- [The loader is dependency-injected (`Load(args, lookupEnv)`) so tests never
  read the real environment or `/etc`
- [An environment override that is set to empty is an error, never a silent fall
  back to the file or the default

Changes:
- go.mod, go.sum: new module and the toml dependency
- internal/config/config.go, internal/config/config_test.go: the loader and tests
- cmd/tcp-wake/main.go: entry point that loads config and exits non-zero on error
- docs/implementation-plan.md: ticked T2
- AGENTS.md: recorded the config-loader facts and the now-runnable test command

Verification:
- `gofmt -l .` clean; `go build ./...` and `go vet ./...` pass; `go test ./...` green
- `python3 scripts/check_traceability.py docs/specs/SRS.md docs/architecture.md` exits 0

Next steps:
- T3 — Listener and Intake (accept, retain raw bytes, framing, cap, no deadlines)
- T4 — Health state and Probe; T5 — Wake trigger
## [1] Landed the tcp-wake Go module and a validated, env-overridable TOML config loader.
Created the `tcp-wake` module (Go 1.27.1) and the config loader the whole service will read its key set from, satisfying IF-5 and ADR-0011/0013/0015, because every later building block depends on that key set being resolved and validated before anything starts. The loader resolves the file by `--config` -> `$TCPWAKE_CONFIG` -> the fixed `/etc/tcp-wake/config.toml` with no working-directory fallback, overlays `TCPWAKE_<KEY>` environment variables so the environment wins over the file, and validates every duration and size at start. Precedence is a merge over raw strings (defaults -> file -> env -> parse) rather than a presence bitmask, which keeps the three sources and the parse step separate and testable. A missing, unreadable, unknown-key, or malformed file prevents start with a message naming the key, and an env override set to empty is an error rather than a silent fallback. Fifteen unit tests plus mutation checks cover per-key file changes, discovery precedence, env-wins, malformed values from both sources, and that the code defaults and TOML tags match `docs/config.example.toml`.

Steps taken:
- Ran `go mod init tcp-wake` (go 1.27.1) and added the one permitted dependency, `github.com/BurntSushi/toml v1.6.0`
- Implemented `internal/config` with a typed `Config`, a `ByteSize` IEC parser, a prefilled `rawConfig`, a shared `keyField` binding table, `ResolvePath`, and `Load(args, lookupEnv)`
- Added `cmd/tcp-wake/main.go` to load config and exit non-zero on failure
- Wrote 15 tests including discovery order, env-wins, empty-override rejection, malformed durations/sizes, unknown key, syntax error, and a reflection guard pinning defaults/tags to the example file
- Mutation-tested the suite: disabling env overrides and swallowing malformed durations each turned the expected tests red, then restored
- Ran `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test -count=1 ./...` and the traceability checker, all clean; ticked T2, updated logs/AGENTS.md, committed and pushed

Decisions:
- [Use `BurntSushi/toml`, the single small third-party dependency ADR-0013 permits
- [Keep the code defaults pinned to `docs/config.example.toml` by a test so the file and code cannot drift
- [Implement precedence over raw strings: defaults -> file -> env -> parse, so "environment wins" is a property of the merge
- [Inject `lookupEnv` into `Load`/`ResolvePath` so tests never read the real environment or `/etc`
- [Treat a set-but-empty environment override as an error, never a silent fallback
- [Decode into a pre-filled struct (BurntSushi only overwrites present keys) and reject unknown keys via `MetaData.Undecoded()`

Changes:
- go.mod: new module `tcp-wake` on Go 1.27.1 with the toml requirement
- go.sum: checksums for `github.com/BurntSushi/toml v1.6.0`
- internal/config/config.go: config types, IEC `ByteSize` parser, path resolution, env overlay, and start-time validation
- internal/config/config_test.go: 15 tests covering keys, precedence, discovery, malformed input, and example-file parity
- cmd/tcp-wake/main.go: entry point that loads config and exits non-zero on error
- docs/implementation-plan.md: ticked T2
- docs/log.md: appended T2 entries
- AGENTS.md: recorded the loader contract, TOML merge mechanics, lint/test commands, and fixed the stale specs path

Next steps:
- T3 — Listener and Intake: accept, retain raw request bytes, framing, `held_body_cap` 413, and no read/write deadlines
- T4 — Health state and Probe against `health_path` at `probe_interval`/`probe_timeout`
- T5 — Wake trigger: exec `wake_command` once per held request and report its exit status
- T6 — Wait-bound timer measured from arrival and never restarted

## T3 Complete — Listener and Intake

Implemented the entry of the request data path in a new `internal/proxy` package:
the Listener accepts on `listen_address`, Intake retains one request as the exact
wire bytes the client sent, and the HeldSet tracks what is currently held.
Satisfies FR-2, FR-6, FR-7, FR-13, FR-20, IF-1, and NFR-4, and carries ADR-0001
(goroutine-per-connection), ADR-0004 (raw wire bytes under the cap), and ADR-0006
(plain HTTP).

Steps taken:
- Added `request.go`: `Pending` (Conn, exact Bytes, Arrival, `Discarded()` channel, idempotent `Discard()` via `sync.Once`).
- Added `heldset.go`: mutex-guarded, uncapped `HeldSet` with `Add`/`Remove`/`Len`/`All`/`CloseAll` (ADR-0012, FR-16).
- Added `intake.go`: reads header lines with `ReadBytes('\n')` so nothing is read ahead, inspects only `Content-Length`/`Transfer-Encoding` for framing, copies the body verbatim (exact read or chunk framing incl. extensions/trailers). An over-cap `Content-Length` is refused before the body is read; a chunked body is counted against the cap as it arrives, so at most the cap is buffered.
- Added `errors.go`: the shared ADR-0014 JSON writer, used for the 413 naming `held_body_cap` and the limit; T8 reuses it for the other three.
- Added `listener.go`: injectable `net.Listen`, per-connection goroutine, Intake then handler; a blocking-Read close watcher discards a request whose client disconnects (FR-7); no deadline is ever set (NFR-4).
- Wired `cmd/tcp-wake/main.go` to run the listener with a hold-only handler, marked TODO for T4/T5/T7.
- Wrote 13 tests: byte-exact retention (odd casing/order, chunked, bodyless GET), the cap boundary and both over-cap paths, framing errors with no response, client-close discard, no bytes while held, listener hand-off, the NFR-5 floor of 8 concurrent holds, and two NFR-4 checks (runtime deadline spy + source scan).
- Smoke-tested the binary: a request while "not healthy" is held with zero response bytes.
- Mutation-tested: disabling the cap check, the close watcher, or header preservation, and setting a deadline, each turned the expected tests red; restored.

Decisions:
- [Framing without parsing: only the header/body boundary and the two framing fields are inspected; the retained bytes are the wire bytes.
- [Client close is detected by a blocking Read, not a deadline, so NFR-4 holds.
- [The 413 uses the ADR-0014 envelope now, so T8 reuses one writer instead of rewriting.
- [Framing errors close the connection with no synthetic response; the four JSON bodies are the only responses.
- [The cap is measured on body bytes, per FR-20 and the `held_body_cap` comment.
- [`internal/proxy` hosts Listener, Intake, and HeldSet together to avoid import cycles and a split owner for the held set.

Changes:
- internal/proxy/request.go, heldset.go, intake.go, errors.go, listener.go: new package
- internal/proxy/{intake,listener,deadline,helpers}_test.go: 13 tests
- cmd/tcp-wake/main.go: runs the listener with a hold-only handler
- docs/implementation-plan.md: ticked T3
- docs/log.md: appended this entry
- AGENTS.md: recorded the intake/framing, close-detection, and test-wiring facts

Next steps:
- T4 — Health state and Probe against `health_path` at `probe_interval`/`probe_timeout`
- T5 — Wake trigger: exec `wake_command` once per triggering request, report its exit status
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release

## [3] Listener and Intake — accept requests and retain them byte-exactly under the cap

Implemented the entry of the request path in a new `internal/proxy` package so held, woken, and forwarded requests have a byte-exact source. The Listener accepts on `listen_address`, Intake retains one request as the exact wire bytes the client sent, and the HeldSet tracks what is currently held, satisfying FR-2/6/7/13/20, IF-1, and NFR-4. Framing is detected without parsing: only `Content-Length`/`Transfer-Encoding` are inspected, so header order and casing survive (ADR-0004), and the body cap gets the shared ADR-0014 413. A client close is observed by a blocking `Read` rather than a deadline, because NFR-4 forbids deadlines on held connections. `cmd/tcp-wake` now runs the listener with a hold-only handler, ready for T4/T5/T7 to replace it.

Steps taken:
- Added `request.go` with `Pending` (exact `Bytes`, `Arrival`, idempotent `Discard()`/`Discarded()`).
- Added `heldset.go` with a mutex-guarded, uncapped `HeldSet` and `CloseAll` drain.
- Added `intake.go` reading header lines with `ReadBytes('\n')` (nothing read ahead), framing-only field inspection, verbatim exact/chunked body reads, and body-byte cap enforcement.
- Added `errors.go` with the shared ADR-0014 JSON writer for the 413 naming `held_body_cap`.
- Added `listener.go` with injectable `net.Listen`, per-connection goroutine, blocking-Read close watcher, and context shutdown.
- Wired `cmd/tcp-wake/main.go` to serve with a hold-only handler.
- Wrote 13 tests and ran four mutations; smoke-tested the binary holds a request with zero response bytes.

Decisions:
- [Retain raw wire bytes and inspect framing only; never parse into a request object.
- [Detect client close with a blocking `Read`, not a deadline, to keep NFR-4.
- [Emit the 413 in the ADR-0014 envelope now, so T8 reuses one writer instead of rewriting.
- [Close the connection with no synthetic response on a framing error; the four JSON bodies are the only responses (ADR-0009).
- [Measure the cap on body bytes, per FR-20 and the `held_body_cap` comment.
- [Host Listener, Intake, and HeldSet together in `internal/proxy` to avoid import cycles and a split owner for the held set.

Changes:
- internal/proxy/request.go: new `Pending` type with discard signalling
- internal/proxy/heldset.go: new `HeldSet`
- internal/proxy/intake.go: verbatim intake, framing detection, cap enforcement
- internal/proxy/errors.go: shared ADR-0014 error writer (413 used, T8 reuses)
- internal/proxy/listener.go: listener, per-connection serve, close watcher, shutdown
- internal/proxy/intake_test.go, listener_test.go, deadline_test.go, helpers_test.go: 13 tests named for their requirement IDs
- cmd/tcp-wake/main.go: runs the listener with a hold-only handler (T4/T5/T7 replace it)
- docs/implementation-plan.md: ticked T3
- docs/log.md: appended the T3 entries
- AGENTS.md: recorded the intake/framing, close-detection, cap-scope, and test-wiring learnings

Next steps:
- T4 — Health state and Probe against `health_path` at `probe_interval`/`probe_timeout`
- T5 — Wake trigger: exec `wake_command` once per triggering request, report its exit status
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release

## [4] Health state and Probe — a two-state belief written only by a probe

Implemented the health belief and the probe that is its only writer. `Health` holds healthy/not healthy and starts not healthy, matching a fresh process; `observe(ready)` is unexported, so no other block—the forwarder included—can infer the state from a failure's cause (ADR-0008). `WaitHealthyOr(ctx, done)` blocks until the belief is healthy, the request is discarded, or shutdown, using a close-and-replace broadcast channel so a waiter cannot miss a transition. `Prober` polls `health_path` on `target_address` at `probe_interval` with `probe_timeout` while a request is pending, exits on the first ready observation so a healthy target is never probed on a timer, and stops when the last pending request leaves (FR-12). Ready is HTTP 200 with body `{"status":"ok"}`; any other status, any other body, a timeout, or a transport error is not healthy (FR-10, FR-11, IF-3). `ProbeNow` runs one probe and sets the state from the result—the FR-19 call the forwarder will make after a 502. `cmd/tcp-wake` now wires the belief and prober into the hold-only handler, which releases on the first ready observation and still writes nothing until T5/T6/T7 replace it.

Steps taken:
- Added `internal/proxy/health.go`: `Health` (mutex-guarded belief, unexported `observe`, `WaitHealthyOr`) and `Prober` (pending-gated cadence loop, `ProbeNow`, `Close`).
- Added `internal/proxy/probe.go`: `httpProbe` with a per-attempt fresh connection, `probe_timeout` via context, a 256 B body cap, and the exact ready-body check.
- Wired `cmd/tcp-wake/main.go` to `prober.RequestStarted`/`RequestDone` and `health.WaitHealthyOr(ctx, p.Discarded())`, holding when not healthy.
- Wrote 11 tests and ran two mutations (loop that does not exit on ready; loop that does not stop when pending drops); both turned the expected test red.
- Smoke-tested: zero probes while idle; probes to `/health` begin only once a request is held; the client receives zero bytes while held; the connection releases when `/health` turns ready.

Decisions:
- [Kept `observe` unexported so ADR-0008's "observation is the only writer" is structural, not conventional.
- [Started the cadence loop only for the first pending request and never while already healthy, so a healthy target gets no timer probes (ADR-0008, FR-12).
- [Probed immediately at loop start, then every interval; ADR-0007's worst case (interval + timeout) is unaffected and the common case detects a just-became-ready target sooner.
- [Exited the loop on the first ready observation; a later transport failure re-arms probing through `ProbeNow` and the next request.
- [Used a close-and-replace channel for the transition broadcast to avoid missed wakeups under the mutex.
- [Disabled HTTP keep-alives for the probe because the target may be mid-boot and a reused socket would be misleading.
- [Bounded the health body read at 256 B and compared with `strings.TrimSpace`, tolerating only surrounding whitespace per IF-3.

Changes:
- internal/proxy/health.go: new `Health` and `Prober`
- internal/proxy/probe.go: new `httpProbe`
- internal/proxy/health_test.go: 11 tests named for FR-10/FR-11/FR-12/FR-19/IF-3 and the ADR-0008 rule
- cmd/tcp-wake/main.go: hold-only handler now waits on the health belief
- docs/implementation-plan.md: ticked T4
- docs/log.md: appended this entry
- AGENTS.md: recorded the T4 learnings

Next steps:
- T5 — Wake trigger: exec `wake_command` once per triggering request, report its exit status
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release

## [4] Health state and Probe — a two-state belief written only by a probe

Implemented T4: the health belief about hypha and the probe that is its only writer. `Health` starts not healthy and changes only through an unexported `observe(ready)`, so no block (the forwarder included) can infer the state from a failure's cause (ADR-0008). `Prober` polls `health_path` on `target_address` at `probe_interval` with `probe_timeout` only while a request is pending (FR-12), exits on the first ready observation so a healthy target is never probed on a timer, and `ProbeNow` provides the single-probe FR-19 seam for the forwarder. Ready is HTTP 200 with body {"status":"ok"}; any other status, body, timeout, or transport error is not healthy (FR-10, FR-11, IF-3). `cmd/tcp-wake` now wires the belief into the still-hold-only handler, which releases on the first ready observation and writes nothing until T5/T6/T7 land.

Steps taken:
- Added `internal/proxy/health.go` with `Health` and the pending-gated `Prober` cadence loop.
- Added `internal/proxy/probe.go` with the context-bounded, keep-alive-disabled HTTP probe.
- Wired `cmd/tcp-wake/main.go` to `RequestStarted`/`RequestDone` and `health.WaitHealthyOr(ctx, p.Discarded())`.
- Wrote 11 tests named for FR-10/FR-11/FR-12/FR-19/IF-3 and the ADR-0008 rule.
- Ran two mutation checks (loop not exiting on ready; loop not stopping at pending 0) that each turned the expected test red.
- Smoke-tested: zero probes idle, probes to `/health` while held, zero client bytes, release once ready.
- Ticked T4, appended the log entry, and recorded the learnings in AGENTS.md.

Decisions:
- [Kept `observe` unexported so ADR-0008's "observation is the only writer" is structural, not conventional.
- [Started the loop only on the 0->1 pending edge and only while not healthy; stopped it at pending 0 and on first ready (FR-12, ADR-0008).
- [Probed immediately at loop start, then every interval; ADR-0007's worst case is unaffected and a just-became-ready target is detected sooner.
- [Used a close-and-replace broadcast channel in `WaitHealthyOr` to avoid missed wakeups.
- [Disabled HTTP keep-alives and capped the health body read at 256 B.
- [Compared the ready body with `strings.TrimSpace` to tolerate only surrounding whitespace per IF-3.

Changes:
- internal/proxy/health.go: new `Health` (belief, `WaitHealthyOr`) and `Prober` (pending-gated loop, `ProbeNow`, `Close`)
- internal/proxy/probe.go: new `httpProbe`
- internal/proxy/health_test.go: 11 tests for FR-10/FR-11/FR-12/FR-19/IF-3 and the ADR-0008 rule
- cmd/tcp-wake/main.go: hold-only handler now waits on the health belief
- docs/implementation-plan.md: ticked T4
- docs/log.md: appended the T4 entry
- AGENTS.md: recorded the T4 learnings and the probe-path smoke test

Next steps:
- T5 — Wake trigger: exec `wake_command` once per triggering request, report its exit status
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release


## [5] Wake trigger — one execution and one log line per not-healthy request

Implemented T5: the wake trigger that powers hypha on. `WakeTrigger` execs `wake_command` once for each request received while the target is not healthy, reads the exit status, and reports both a non-zero exit and a launch failure (FR-3, FR-17, IF-4). The command is a single path run directly as a child process — no shell, no argument splitting, stdout and stderr discarded — because ADR-0005 makes the elevated privilege a property of the setuid-root file itself and a shell string would both break setuid and invent an argument grammar nothing specifies. `Logger` writes exactly one greppable line per execution with an RFC3339 timestamp and the status (IF-6, ADR-0010); T9 adds the error line to the same type. `Pipeline` is the request path: a not-healthy request wakes, logs, and on failure returns an immediate 500 naming `wake_command` before any probe or hold; on success it starts the probe and holds. `Pipeline` lives in `internal/proxy` because `writeError` is package-private; T6 and T7 extend `Handle`.

Steps taken:
- Added `internal/proxy/wake.go` with `WakeResult` and `WakeTrigger`.
- Added `internal/proxy/log.go` with the narrow `Logger` (wake line only).
- Added `internal/proxy/pipeline.go` with the request path `Handle`.
- Rewired `cmd/tcp-wake/main.go` to build the logger, the wake trigger, and the pipeline, and to pass `pl.Handle`.
- Wrote 9 tests (unit and end-to-end) covering IF-4, FR-3, FR-12, FR-17, IF-6, the direct-exec/no-shell rule, and NFR-7's trigger half.
- Ran two mutations (skip the FR-17 500; double the log line); each turned the expected test red.
- Fixed a `-race` data race in the test's own log buffer with a mutex-guarded `syncBuffer`.
- Smoke-tested: two held requests produced two wake executions and two log lines; a failing command produced a 500 naming `wake_command` and one line with `status=7`.

Decisions:
- [Treated `wake_command` as one path, exec'd directly; no shell and no argument splitting (ADR-0005 makes privilege a file property, and setuid scripts do not work).
- [Discarded the command's stdout/stderr so only the one ADR-0010 line describes the execution.
- [Reported a launch failure (`ExitCode == -1`, `status=launch-failed`) the same way as a non-zero exit, so FR-17 covers a missing or unexecutable command.
- [Ran the trigger per request and never serialised it, matching FR-3 and RS-6's per-request failure model.
- [Added only the wake line to `Logger`; the error line and the content inspection stay T9's, so T9 extends the type rather than duplicating it.
- [Put the request path in `Pipeline` inside `package proxy` so the 500 can use the existing package-private `writeError`; main only wires and passes `Handle`.

Changes:
- internal/proxy/wake.go: new `WakeResult` and `WakeTrigger`
- internal/proxy/log.go: new `Logger` with the wake line
- internal/proxy/pipeline.go: new `Pipeline.Handle` (wake, 500, hold)
- internal/proxy/wake_test.go: tests for IF-4, FR-3, FR-12, FR-17, IF-6, the no-shell rule, NFR-7
- internal/proxy/helpers_test.go: added the mutex-guarded `syncBuffer`
- cmd/tcp-wake/main.go: build logger/wake/pipeline and serve `pl.Handle`
- docs/implementation-plan.md: ticked T5
- docs/log.md: appended this entry
- AGENTS.md: recorded the T5 learnings

Next steps:
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release

## [5] SOLID review cleanup — remove dead surface and tighten naming

Reviewed the whole package against SOLID and cleaned the accumulated mess. The
main finding was dead surface: `WakeResult.Command` was written but never read
(the logger takes the command separately), `Pipeline` stored a `*config.Config`
it never used, `HeldSet.All` was never called, and `Handler` returned an `error`
that `serveConn` discarded. The held set was also exported although it is an
implementation detail of the Listener, and the Listener constructor was the
ambiguous `New` in a package with six constructors. Fixed all of these, updated
the package doc to name the health/probe/wake/pipeline blocks, and made the test
config carry valid cadences so a test that starts the real Prober loop cannot
panic on `time.NewTicker(0)`. No behaviour changed; `go test -race` and the
traceability check stay green.

Steps taken:
- Removed `WakeResult.Command` and the unused `Pipeline.cfg` field (and its `NewPipeline` parameter).
- Removed the unused `HeldSet.All`, unexported the set to `heldSet` (`newHeldSet`, `add`, `remove`, `count`, `closeAll`), and replaced `Listener.Held()` with `heldCount()`.
- Renamed `proxy.New` to `proxy.NewListener`; updated `main` and the tests.
- Changed `Handler`/`Pipeline.Handle` to return nothing, since no caller used the error.
- Updated the `package proxy` doc comment to describe every block.
- Gave `testConfig()` a positive `ProbeInterval`/`ProbeTimeout` to remove a latent `time.NewTicker` panic.
- Recorded the naming/exposure rules in AGENTS.md.

Decisions:
- [Removed the `Handler` error return rather than inventing a consumer: the pipeline writes responses and logs inside `Handle`, so the return was dead (ISP).
- [Unexported the held set: nothing outside the package observes it, and the Listener already owns it (encapsulation).
- [Named the constructor `NewListener` for clarity now that the package has several types (SRP in naming).
- [Did not pre-empt T8's error-response consolidation or T6's wait-bound field; left those to their tasks to avoid rework.

Changes:
- internal/proxy/heldset.go: unexported `heldSet`, dropped `All`
- internal/proxy/listener.go: `NewListener`, `heldSet`, `heldCount`, `Handler` without error
- internal/proxy/pipeline.go: dropped `cfg`, `Handle` returns nothing
- internal/proxy/wake.go: dropped `WakeResult.Command`
- internal/proxy/request.go: updated package doc
- internal/proxy/helpers_test.go: valid test cadences; handler signature
- internal/proxy/{listener,deadline,wake}_test.go: updated call sites
- cmd/tcp-wake/main.go: `proxy.NewListener`, `NewPipeline` without cfg
- AGENTS.md: naming/exposure rules
- docs/log.md: appended this entry

Next steps:
- T6 — Wait-bound timer measured from arrival and never restarted
- T7 — Forwarder: verbatim upstream write, streamed relay, parallel release
