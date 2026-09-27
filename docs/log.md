
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
