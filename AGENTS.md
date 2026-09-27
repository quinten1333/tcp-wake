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
- **Don't commit build artifacts.** T1's `stub`/`stub.c` are gitignored.
