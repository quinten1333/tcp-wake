# Implementation Plan — tcp-wake

> Conforms to `specs/SRS.md` v0.1 and `architecture.md` (13 ADRs, all accepted).
> Every task names the requirement IDs it satisfies and the command or observation that proves it.

## Order and why

T1 blocks everything: if the setuid bit does not survive the container, ADR-0003 and ADR-0005 are both wrong and the code written against them is wasted. Verify it before writing Go.

T2 through T10 follow the dependency order of the building blocks in `architecture.md` §5.2. T3, T4, and T5 are independent once T2 lands. T7 depends on T3 (retained bytes), T4 (the state), and T6 (the timer).

## Phase 0 — Blocking prerequisite

- [x] **T1 — Verify the setuid bit survives the container (R-1, ADR-0003, ADR-0005, NFR-7).**
  Build a minimal container that bind-mounts a setuid-root stub, start it, and check the bit from *inside*:
  `ls -l` shows `4755 root root` and `id` shows a non-root uid. Also confirm the mount is not `nosuid`.
  **If this fails, stop and reopen ADR-0003 and ADR-0005** — do not proceed to T2 with a plan that cannot work.
  Verify: the two commands above, output recorded in the README.

## Phase 1 — Core implementation

- [x] **T2 — Go module, config loader (IF-5, ADR-0011, ADR-0013).**
  Module `tcp-wake`; `Config` struct with TOML tags matching `config.example.toml`; resolve the file by `--config`, then `$TCPWAKE_CONFIG`, then the fixed default `/etc/tcp-wake/config.toml` (ADR-0015); environment overrides as `TCPWAKE_<KEY>` that win over the file; parse and validate durations and sizes at start; a missing, unreadable, or malformed file prevents start with a message naming the key.
  Verify: `go build ./...`, `go vet ./...`, and unit tests for each key, for the override precedence, and for a malformed duration failing start.

- [x] **T3 — Listener and Intake (FR-2, FR-6, FR-7, FR-13, FR-20, IF-1, NFR-4, ADR-0001, ADR-0004).**
  Accept on `listen_address` and return the response over the same connection (IF-1); retain the request as raw wire bytes; detect completion by framing without parsing; enforce `held_body_cap` with a 413 naming the cap; discard a held request whose client closes its connection (FR-7); set no read or write deadline anywhere.
  Verify: `go test ./...` — byte-exact retention of a request with odd header casing and order; a body over the cap gets 413; a held connection has no deadline set; a client that closes while held leaves the held set and is never forwarded.

- [x] **T4 — Health state and Probe (FR-10, FR-11, FR-19, IF-3, ADR-0007, ADR-0008).**
  Probe `health_path` on `target_address` at `probe_interval` with `probe_timeout` while a request is pending; ready is 200 with body `{"status":"ok"}`; any other status or a timeout means not healthy; the state changes only from a probe result, never inferred from a failure's cause.
  Verify: `go test ./...` — a fake target answering 200/503/timeout drives the state correctly; no timer runs while no request is pending (FR-12).

- [x] **T5 — Wake trigger (FR-3, FR-12, FR-17, IF-4, NFR-7, ADR-0005, ADR-0010).**
  Exec `wake_command` once per request received while not healthy; read the exit status; write one log line per execution; a non-zero exit gives that request's client a 500 naming the wake command, immediately.
  Verify: `go test ./...` — a stub command records its invocations; 5 requests in one boot window produce 5 executions; a stub exiting non-zero produces one 500 and one log line.

- [x] **T6 — Wait-bound timer (FR-8, NFR-1, ADR-0009).**
  Per request, measured from arrival; never restarted by a wake attempt; on expiry the client receives 504 naming the bound.
  Verify: `go test ./...` — a request whose target never answers gets 504 after the bound; a second wake attempt does not extend it.

- [x] **T7 — Forwarder (FR-1, FR-4, FR-5, FR-13, FR-14, FR-15, FR-18, IF-2, NFR-2, NFR-3, NFR-5, ADR-0004).**
  Forward a held request to `target_address` over HTTP and relay the response (IF-2); write held bytes verbatim; relay the response verbatim, streaming each chunk as it arrives; on the state becoming healthy, unblock every held request so each opens its own upstream connection without waiting for the others; a transport-level failure gives that client a 502 and triggers a probe; hold at least 8 concurrent requests without discarding any (NFR-5).
  Verify: `go test ./...` — byte-exact forward and response; 3 held requests open 3 upstream connections within 100 ms of each other; a 100-chunk stream arrives unbuffered; a dropped target gives one 502 and then a probe; 8 concurrent requests during one boot window all receive a response.

- [x] **T8 — Error responses (NFR-6, ADR-0009).**
  The four JSON bodies, each naming the component or limit: 504 the wait bound, 500 the wake command, 502 the unreachable target, 413 the body cap. These four are the only responses the system itself produces, so together with T7's relay they must yield exactly one response per accepted request whose client stays connected (NFR-6).
  Verify: `go test ./...` — each of the four is triggered and its body asserted; the response count across the suite equals the accepted count minus client-abandoned requests.

- [ ] **T9 — Logger (IF-6, FR-3, FR-9, FR-12, ADR-0010).**
  Text lines to stdout, one per wake execution and one per error; no request or response content ever written.
  Verify: `go test ./...` — line counts match; inspection confirms no body content appears.

- [ ] **T10 — Restart semantics (FR-16, §2 non-goal 5).**
  All state in-memory; a fresh process starts not healthy with no held set; nothing is persisted.
  Verify: `go test ./...` — restart mid-hold forwards nothing for the held requests.

## Phase 2 — Acceptance suite

- [ ] **T11 — Integration harness.**
  A fake target that can be started, stopped, and made to answer 503; a stub wake command that records invocations and can be made to fail; a fake client that can hold and disconnect.
  Verify: the harness runs green against a stub implementation and fails when a behaviour is removed.

- [ ] **T12 — The 34 requirement checks as executable tests.**
  One test per requirement, named for its ID, carrying the check from `specs/SRS.md`. The countable ones (FR-3, FR-9, FR-12) read the log lines; the inspection ones (NFR-4, NFR-7, NFR-8) are documented checklists in the README.
  Verify: `go test ./...` green; a deliberately broken behaviour turns exactly the expected test red.

- [ ] **T13 — `specs/RTM.md`.**
  One row per requirement: `req_id, requirement, source, priority, verify_method, test_id, status`, with status moving from Draft to verified as tests land.
  Verify: every requirement ID in `specs/SRS.md` appears exactly once.

## Phase 3 — Deployment

- [ ] **T14 — Container and compose.**
  Image with host networking; config mounted at `/etc/tcp-wake/config.toml`; the wake command bind-mounted from the host; no elevated capabilities for the proxy process; restart on failure.
  Verify: `docker exec` shows the setuid bit intact (T1's check, now repeatable) and a non-root uid.

- [ ] **T15 — Deployment check as a script.**
  T1's verification turned into a command the operator runs after any redeploy, so R-1 cannot regress silently.
  Verify: the script exits non-zero when the bit is stripped.

- [ ] **T16 — README.**
  Install, the routing entry to add (one entry for hypha, nothing else touched), the config key reference, the two inspection checklists (NFR-4, NFR-7, NFR-8), and the accepted risks the operator must not try to fix.
  Verify: a reader who has not seen this conversation can deploy and verify it.

## Do not build

Each of these is out of scope by a decision recorded in the spec or an ADR. Building one is a defect, not a bonus.

| Tempting | Why not |
|---|---|
| A connection timeout on held requests | NFR-4 forbids one; §2 non-goal 7 puts timeouts on llama.cpp and the routing layer |
| A cap on the number of held requests | ADR-0012 chose an unbounded set with a tested floor of 8 |
| Shutting hypha down | §2 non-goal 1; hyperion has no login path to hypha (C-3) |
| Waiting for a model to load | §2 non-goal 2; llama-server's router mode answers health before a model is resident (C-5) |
| TLS termination | ADR-0006; the existing routing owns it |
| Client authentication | §2 non-goal 6; the routing layer is the trust boundary |
| Persisting held requests across a restart | §2 non-goal 5; FR-16 requires they are not forwarded |
| Probing before every forward | ADR-0008 chose the cached belief with FR-18/FR-19 as the correction |
| Logging request or response bodies | §2 non-goal 3; ADR-0010 logs wake and error lines only |
| A retry loop around the wake command | FR-17 fails that request immediately; each request triggers its own execution |
