# Architecture — tcp-wake

> Conforms to: `specs/SRS.md` baseline v0.1 (traceability: `specs/DECISION_MAP.md`).
> All 13 ADRs (§9) are `accepted`. Every design assertion below cites its ADR.
> One thing the requirements force with no alternative, and which is therefore not an ADR: all state is in-memory and dies with the process, because §2 non-goal 5 puts holding requests across a restart out of scope.

## 1. Introduction and Goals

- **System in a paragraph** (SRS §1): a reverse-proxy service on the always-on host **hyperion**, sitting in front of **hypha**, an LLM host that is normally powered off to save electricity. A request arriving while hypha is off is held open with no error response, the configured wake command runs, hypha's health endpoint is polled, and every held request is forwarded as soon as hypha reports healthy.
- **Goal** (one sentence, verbatim from SRS §1): "The system shall deliver every client request bound for hypha to hypha — waking hypha when it is off and holding the request until hypha is healthy — so that no client sees an error caused by hypha being off."
- **Quality goals** (full six-part scenarios in §10.1):
  1. **Performance** — the first response after an offline request arrives within 120 s (NFR-1).
  2. **Performance** — a request while hypha is healthy is answered within 2 s, and streamed chunks gain no more than 50 ms (NFR-2, NFR-3).
  3. **Reliability** — exactly one response per accepted request whose client stays connected (NFR-6).
  4. **Security** — only the wake command runs with elevated privileges (NFR-7, NFR-8).
  5. **Availability** — hypha stays off while nothing is pending (G4, via FR-12).
- **Baseline**: SRS `specs/SRS.md` v0.1, reviewed and confirmed by the user. Change control: any change to an ADR or a section requires an impact assessment and a re-run of the traceability check on the affected requirements and ADRs.

## 2. Constraints

| Constraint (SRS §2) | Respected by (ADR / element) | If unmet, the risk |
|---|---|---|
| C-1 — hyperion is the only always-on host; hypha is normally off | ADR-0002 (one route among many), ADR-0003 (deployed like everything else on hyperion) | The system becomes a second always-on dependency to keep alive |
| C-2 — hypha's boot time is ≈20 s | ADR-0007 (≈3 s detection inside a 120 s budget) | A slower boot erodes NFR-1's margin (R-2) |
| C-3 — hyperion has no login path to hypha | ADR-0002, ADR-0005 (the only privileged action is a wake, never a session) | Any credential on hyperion would widen the trust boundary |
| C-4 — the wake command needs elevated privileges | ADR-0005 (setuid command), ADR-0003 (container must preserve the bit) | The wake path silently breaks (R-1) |
| C-5 — llama-server in router mode answers health before a model loads | ADR-0007 (the probe is the only health signal) | A model-load wait would enter the hold window, which non-goal 2 forbids |
| §2 accepted risk — hypha's own idle-shutdown can race the wake path | Nothing; explicitly accepted, not mitigated | A shutdown during a hold is indistinguishable from a slow boot (R-3) |

## 3. Context and Scope

- **In scope**: the hypha-bound request path — hold, wake trigger, health detection, release, forwarding, and the four error paths.
- **Out of scope** (SRS §2 non-goals): powering hypha off; waiting for a model to load; inspecting, transforming, caching, or storing content; any wake mechanism other than the configured command; holding requests across a restart; client authentication; connection timeouts.
- **Context**: the system is a black box between hyperion's existing routing and hypha. Outside it are the client, the routing layer that decides which service a request is for, hypha itself, and the wake command.

```
 ┌────────┐   Ch-1: HTTP    ┌──────────────────────┐   Ch-2: HTTP   ┌──────────────┐
 │ client │────────────────▶│ existing routing     │───────────────▶│  tcp-wake    │
 │ (agent,│                 │ (hyperion, owns TLS  │                │  (this system│
 │ editor)│                 │  and auth; one route │                │   container) │
 └────────┘                 │  for hypha — ADR-0002│                └──────┬───────┘
      ▲                     └──────────────────────┘                       │
      │                              other vhosts: unchanged (FR-9)        │
      │                                                                     │
      │  response bytes (unchanged, streamed — FR-14, FR-15)                │
      └─────────────────────────────────────────────────────────────────────┘
                                                                           │
                                        Ch-3: health probe (GET /health)   │
                                        Ch-4: forward (held, verbatim)     │
                                        Ch-5: exec wake command            │
                                                                           ▼
                                                    ┌──────────────────────────────────┐
                                                    │ hypha (normally off)             │
                                                    │  /health = readiness (C-5)       │
                                                    └──────────────────────────────────┘
                                                    wake command: on hyperion, setuid-root
```

- **External interfaces**:

| Interface | Ends | Crosses | Format | On failure |
|---|---|---|---|---|
| Ch-1 client | client ↔ system listener | request bytes in; response bytes out | HTTP over TCP, byte-identical both ways (FR-13, FR-14) | no bytes while held (FR-6); four error paths (§5.1) |
| Ch-2 routing | existing routing ↔ system | the hypha-bound route | TCP, plain HTTP (ADR-0006) | a request for another service never arrives (FR-9) |
| Ch-3 probe | system ↔ hypha `/health` | synthetic `GET /health` out; status and body in | HTTP; 200 + `{"status":"ok"}` is ready (IF-3) | not ready, state stays or becomes not healthy (FR-11) |
| Ch-4 forward | system ↔ hypha | held bytes out; response bytes in | HTTP over TCP, one connection per held request (FR-5) | 502 to that client, then a probe (FR-18, FR-19) |
| Ch-5 wake | system ↔ wake command | one exec per triggering request; exit status back | process execution, setuid-root command (ADR-0005) | 500 to that client immediately (FR-17) |

## 4. Solution Strategy

- **Style and why**: a single-process, in-memory request pipeline around a two-state health belief, with one goroutine per held request — ADR-0001. The spec's core behaviours are per-connection (hold with no deadline, parallel release, streamed relay), and a goroutine-per-connection runtime makes those structural rather than configured.
- **Top-level decomposition**:
  - **Listener** — owns the client socket; accepts, and hands the connection to Intake (ADR-0006).
  - **Intake** — retains the request as raw wire bytes and detects when it is complete, without interpreting it; enforces the body cap (ADR-0004).
  - **Health state** — the belief, healthy or not healthy, changed only by an observation (ADR-0008).
  - **Held set** — the requests currently held, one goroutine each (ADR-0001, ADR-0004).
  - **Wait-bound timer** — per request, measured from arrival (FR-8).
  - **Wake trigger** — execs the configured command, once per triggering request (ADR-0005).
  - **Probe** — polls `/health` while a request is pending, and once after a transport failure (ADR-0007, ADR-0008).
  - **Forwarder** — sends held bytes verbatim, relays the response back, streaming (ADR-0004).
  - **Config loader** — file plus environment overrides (ADR-0011).
  - **Logger** — wake and error lines (ADR-0010).
- **Technology choices**: Go (ADR-0001); plain HTTP on a configured address with TLS left to the routing (ADR-0006); container with host networking (ADR-0003); a setuid-root command as the privilege boundary (ADR-0005).
- **Per quality goal**: NFR-1 → hold with no deadline + bounded probe detection + wake trigger (ADR-0004, ADR-0007, ADR-0005). NFR-2 → direct forward with no pre-probe (ADR-0006, ADR-0008). NFR-3 → streaming relay (ADR-0001). NFR-4 → goroutine-per-held-connection with no deadline ever set (ADR-0001, ADR-0004). NFR-5 → unbounded held set with a tested floor of 8 (ADR-0012). NFR-6 → the four error paths are the only exits, each producing exactly one response (ADR-0009). NFR-7/NFR-8 → the setuid boundary and the container specification (ADR-0005, ADR-0003).

## 5. Building Block View

### 5.1 Level 1 — containers

| Block | Responsibility (one sentence) | Interface |
|---|---|---|
| tcp-wake (Go process in a container, ADR-0001, ADR-0003) | Wake-on-demand reverse proxy: hold hypha-bound requests while hypha is not healthy, wake it, and forward verbatim once it is | Ch-1 in/out; Ch-3 out; Ch-4 out/in; Ch-5 out; config in; log out |
| Existing routing (pre-existing, ADR-0002) | Routes the hypha-bound requests to the system and leaves every other vhost alone | Ch-2 in |
| Wake command (host file, setuid-root, ADR-0005) | Powers hypha on, once per invocation | exec in; exit status out |
| Container runtime (host, ADR-0003) | Runs the system's container with host networking | process lifecycle |
| hypha (host, normally off) | Serves the LLM API; answers `/health` when ready | Ch-3 in; Ch-4 in/out |

### 5.2 Level 2 — the blocks worth describing

#### Listener

- **Responsibility**: accept client connections on the configured address and hand each to Intake.
- **Interfaces**: in — TCP on the configured listen address (ADR-0006, ADR-0011); out — a connection with its retained request bytes; errors — a connection that fails framing or exceeds the cap receives 413 (FR-20) or a framing error; no deadline is ever set (NFR-4, ADR-0001).
- **Fulfils**: FR-1, IF-1, NFR-4.
- **Open issues**: none.

#### Intake

- **Responsibility**: retain a request as the exact bytes received, and detect when it is complete without interpreting it.
- **Interfaces**: in — raw request bytes; out — a complete retained request, or an error; errors — body above the cap gets 413 naming the cap (FR-20, ADR-0009); a body that never completes is held indefinitely, by §2 non-goal 7.
- **Fulfils**: FR-2, FR-6, FR-13, FR-20.
- **Open issues**: R-4 (a stalled body is held forever with no defined end).

#### Health state

- **Responsibility**: hold the belief about hypha, and change it only from an observation.
- **Interfaces**: in — a probe result (FR-10, FR-11) or a transport failure (FR-19); out — healthy or not healthy, read by the request path; errors — none; the state is never inferred from a failure's cause (ADR-0008).
- **Fulfils**: FR-10, FR-11, FR-19.
- **Open issues**: none.

#### Held set

- **Responsibility**: keep every request received while hypha is not healthy, and drop one whose client disconnects.
- **Interfaces**: in — a retained request; out — the set of held requests, released together; errors — none; the set is in-memory and dies with the process (§2 non-goal 5), and is uncapped (ADR-0012).
- **Fulfils**: FR-2, FR-4, FR-6, FR-7, FR-16, NFR-5.
- **Open issues**: R-5 (an uncapped set has no defined behaviour under growth).

#### Wait-bound timer

- **Responsibility**: end a held request's wait at the configured bound, measured from the request's arrival.
- **Interfaces**: in — the request's arrival time and the configured bound; out — a 504 naming the bound (FR-8, ADR-0009); errors — the bound is never restarted by a wake attempt, so a request's total wait cannot exceed it.
- **Fulfils**: FR-8, NFR-1.
- **Open issues**: none.

#### Wake trigger

- **Responsibility**: execute the configured command once for each triggering request, and report its exit status.
- **Interfaces**: in — a request received while hypha is not healthy; out — one process execution (ADR-0005), an exit status, and one log line (ADR-0010); errors — a non-zero exit produces an immediate 500 naming the wake command (FR-17).
- **Fulfils**: FR-3, FR-12, FR-17, IF-4, NFR-7.
- **Open issues**: R-1 (a stripped setuid bit breaks the wake path).

#### Probe

- **Responsibility**: determine hypha's health from its health endpoint, and set the state from the result.
- **Interfaces**: in — the configured cadence, timeout, and endpoint path (ADR-0007); out — a state change, and on the first ready answer the release of the held set; errors — any non-ready answer or a timeout means not healthy (FR-11); the probe runs only while a request is pending, and once after a transport failure (FR-19).
- **Fulfils**: FR-10, FR-11, IF-3, NFR-1.
- **Open issues**: none.

#### Forwarder

- **Responsibility**: send a held request's bytes to hypha unchanged, and relay the response back unchanged and unbuffered.
- **Interfaces**: in — a held request's bytes; out — one upstream connection per held request, opened without waiting for any other (FR-5), bytes written verbatim (FR-13); response bytes relayed as they arrive (FR-14, FR-15); errors — a transport-level failure gives that client a 502 (FR-18) and triggers a probe (FR-19).
- **Fulfils**: FR-1, FR-4, FR-5, FR-13, FR-14, FR-15, FR-18, IF-2, NFR-2, NFR-3.
- **Open issues**: none.

#### Config loader

- **Responsibility**: read the key set from the configuration file and apply environment overrides.
- **Interfaces**: in — the TOML file path and the process environment; out — the effective configuration; errors — a missing, unreadable, or malformed file prevents start and the failure names the key (ADR-0013); a key present in both sources takes the environment's value (ADR-0011).
- **Fulfils**: IF-5.
- **Open issues**: none.

#### Logger

- **Responsibility**: write one line per wake execution and one per error.
- **Interfaces**: in — a wake execution or an error; out — text lines on standard output, greppable (ADR-0010); errors — an unwritable log is a start-time failure; no request or response content is ever written (§2 non-goal 3).
- **Fulfils**: IF-6, FR-3, FR-9, FR-12.
- **Open issues**: none.

## 6. Runtime View

### 6.1 RS-1 — request while hypha is healthy (FR-1, NFR-2)

- **Trigger**: a request arrives and the state is healthy.
- **Steps**: 1. Listener accepts; Intake retains the bytes. 2. Health state is healthy, so no hold, no wake. 3. Forwarder opens one upstream connection and writes the bytes verbatim. 4. hypha's response is relayed back as it arrives.
- **Failure case**: the upstream connection fails at the transport level → 502 to that client, then a probe sets the state (RS-7).

### 6.2 RS-2 — first request while hypha is off (FR-2, FR-3, NFR-1)

- **Trigger**: a request arrives and the state is not healthy.
- **Steps**: 1. Intake retains the bytes; the request joins the held set and its wait-bound timer starts. 2. Wake trigger execs the configured command once and logs one line. 3. Probe polls `/health` every 2 s with a 1 s timeout. 4. On the first ready answer the state becomes healthy. 5. Forwarder opens an upstream connection and writes the held bytes verbatim. 6. hypha's response is relayed to the client, streamed if it is a stream.
- **Failure case**: hypha never answers → the wait-bound timer fires at 120 s and the client gets a 504 naming the bound (RS-5). The wake command exits non-zero → 500 naming the command, immediately (RS-6).

### 6.3 RS-3 — more requests during the boot window (FR-4, FR-5, FR-6)

- **Trigger**: further requests arrive while the state is not healthy.
- **Steps**: 1. Each joins the held set with its own timer. 2. Each triggers its own wake-command execution (FR-3). 3. None sends any bytes to its client (FR-6). 4. On the state becoming healthy, every held request's goroutine unblocks and opens its own upstream connection, without waiting for the others (FR-5). 5. Each client receives its own response.
- **Failure case**: one request's forward fails at the transport level → that client gets a 502 and a probe runs; the other held requests are unaffected.

### 6.4 RS-4 — client disconnects while held (FR-7)

- **Trigger**: a held request's client closes its connection.
- **Steps**: 1. Listener observes the close. 2. That request is discarded from the held set. 3. Nothing is forwarded for it.
- **Failure case**: none; the discard touches only that request.

### 6.5 RS-5 — hypha never becomes healthy (FR-8)

- **Trigger**: the wait bound elapses with the state still not healthy.
- **Steps**: 1. The wait-bound timer fires. 2. The client receives 504 with a body naming the bound (ADR-0009). 3. That request leaves the held set.
- **Failure case**: other held requests are unaffected and keep their own timers.

### 6.6 RS-6 — the wake command fails (FR-17)

- **Trigger**: the configured command exits non-zero.
- **Steps**: 1. Wake trigger observes the non-zero exit. 2. The triggering request's client receives 500 immediately, with a body naming the wake command. 3. The error is logged.
- **Failure case**: because each request triggers its own execution, a broken command fails each request on its own execution rather than through any shared state — so the failure is per-request, not a stuck global state (R-6).

### 6.7 RS-7 — a forward fails while the state is healthy (FR-18, FR-19)

- **Trigger**: a forward fails at the transport level — for example hypha was powered off out-of-band after the last probe.
- **Steps**: 1. Forwarder observes the transport failure. 2. That client receives 502. 3. A probe runs immediately. 4. Health state is set from the probe's result. 5. The next request behaves according to that state — held and woken if not healthy, forwarded if healthy.
- **Failure case**: this is the one path where a request can receive an error caused by hypha being off (ADR-0008). It is the accepted cost of not probing before every forward.

### 6.8 RS-8 — restart while requests are held (FR-16)

- **Trigger**: the process restarts.
- **Steps**: 1. All state is in-memory, so it is gone (§2 non-goal 5). 2. Held requests are not forwarded. 3. A fresh process starts not healthy.
- **Failure case**: the held requests' clients observe their connections closing, which is the behaviour the non-goal specifies.

## 7. Deployment View

| Block | Runs on | Notes |
|---|---|---|
| tcp-wake | hyperion — container, host networking | Unprivileged user; no elevated capabilities; restart on failure (ADR-0003) |
| Wake command | hyperion — host file, bind-mounted into the container | mode 4755, root:root; must still carry the bit inside the container (ADR-0005, R-1) |
| Existing routing | hyperion — pre-existing | one entry bound for hypha pointing at the configured listen address (ADR-0002) |
| hypha | hypha host — normally off | `/health` is the readiness signal; the wake target is configuration |
| Container runtime | hyperion — pre-existing | the same mechanism every other service on hyperion uses (ADR-0003) |

Configuration reaches the container as a mounted file plus environment overrides (ADR-0011); with host networking there is no port mapping to keep in step with the listen address.

## 8. Cross-cutting Concepts

### 8.1 Logging

One text line per wake-command execution and one per error, on standard output (ADR-0010). These lines are the counting artefact for FR-3, FR-9, and FR-12. No request or response content is logged (§2 non-goal 3), which is a deliberate trade: faults inside a body are not diagnosable from the system's own logs. Touches Wake trigger and Logger.

### 8.2 Configuration

One TOML file holding the full key set — `listen_address`, `target_address`, `health_path`, `probe_interval` (2s), `probe_timeout` (1s), `wait_bound` (120s), `wake_command`, `held_body_cap` (64MiB) — with environment-variable overrides (`TCPWAKE_<KEY>`) that win over the file (ADR-0013, ADR-0011). The file is located by `--config PATH`, else `$TCPWAKE_CONFIG`, else the fixed default `/etc/tcp-wake/config.toml`, with no working-directory fallback, so the same binary reads the same file wherever it starts (ADR-0015). `target_address` names hypha, the target of both the probe and every forward; "target" was chosen over "origin" because in a proxy "origin" reads from either direction. TOML rather than JSON because the file carries constraints that must be explained beside their values: the setuid requirement on `wake_command`, and the rule that `wait_bound` is measured from arrival and never restarted. Durations are Go duration strings and sizes take an IEC suffix, both validated at start. A missing, unreadable, or malformed file prevents start. The example file is `config.example.toml`. Touches Listener, Intake, Wake trigger, Probe, Wait-bound timer.

### 8.3 Privilege

Only the wake command runs with elevated privileges: it is a setuid-root file, exec'd by the unprivileged proxy process (ADR-0005). The container must preserve the setuid bit, which the container specification alone cannot guarantee (ADR-0003, R-1). Touches Wake trigger. Verified by reading the command's mode from inside the running container, and the proxy process's uid (NFR-7, NFR-8).

### 8.4 Error handling

Four error paths, and they are the only responses the system itself produces; everything else is hypha's response relayed unchanged. Each body names the component or limit responsible (ADR-0009).

| Condition | Status | Body names | Requirement |
|---|---|---|---|
| The wait bound elapses | 504 | the wait bound | FR-8 |
| The wake command exits non-zero | 500 | the wake command | FR-17 |
| A forward fails at the transport level | 502 | hypha as unreachable | FR-18 |
| A body exceeds the held body cap | 413 | the held body cap | FR-20 |

Each body is a JSON object with a nested `error` object carrying `message`, an enumerated `component` (`wait_bound`, `wake_command`, `target`, or `held_body_cap`), and, for the two bounded conditions, a `limit` value — matching hypha's own `{"error":{"message":…}}` envelope so one parser handles both the origin's errors and the proxy's (ADR-0014). For example, a wait-bound expiry is `{"error":{"message":"…","component":"wait_bound","limit":"120s"}}`.

A transport failure additionally triggers a probe, so the state is corrected by observation rather than by inference (FR-19, ADR-0008). The state is never inferred from a failure's cause.

### 8.5 Connection lifetimes

A held connection is open with no response bytes and **no deadline at all** — not a read deadline, not a write deadline (NFR-4, ADR-0001). It ends in one of three ways: the response arrives, the wait-bound timer fires, or the client closes. The system applies no connection timeout of its own anywhere, which is §2 non-goal 7; timeouts belong to llama.cpp and to the routing layer on either side.

### 8.6 Security and data ownership

Client authentication is not this system's job; the routing layer is the trust boundary (ADR-0002, §2 non-goal 6). The listener is plain HTTP, so the system must never be reachable from outside hyperion, or prompts would travel in the clear (ADR-0006, R-7). No state is persisted: everything is in-memory and dies with the process (§2 non-goal 5). The only durable artefacts are the log lines (ADR-0010). The system holds no credential for hypha, which is what keeps C-3 true.

## 9. Architectural Decisions

> The index is rebuilt from the ADR files by `scripts/check_traceability.py --fix` — do not hand-edit between the markers.

<!-- adr-index:start -->
| ADR | Title | Status | Driver | Date |
|---|---|---|---|---|
| ADR-0001 | Go as the proxy implementation | accepted | FR-2, FR-5, FR-15, NFR-3, NFR-4, NFR-5 | 2026-09-23 |
| ADR-0002 | Dedicated listen address behind the existing routing | accepted | C-3, FR-1, FR-9 | 2026-09-23 |
| ADR-0003 | Container with host networking | accepted | C-1, C-4, NFR-7, NFR-8 | 2026-09-23 |
| ADR-0004 | Retain held requests as raw wire bytes under a cap | accepted | FR-2, FR-13, FR-14, FR-15, FR-20, NFR-4, NFR-5 | 2026-09-23 |
| ADR-0005 | setuid-root wake command | accepted | C-4, FR-3, NFR-7, NFR-8 | 2026-09-23 |
| ADR-0006 | Plain HTTP on a configured address, TLS left to the existing routing | accepted | FR-13, IF-1, IF-2 | 2026-09-23 |
| ADR-0007 | Probe cadence 2 s interval with a 1 s timeout | accepted | C-2, C-5, FR-10, FR-11, IF-3, NFR-1 | 2026-09-23 |
| ADR-0008 | Trust the cached health state, and probe only after a transport failure | accepted | FR-1, FR-10, FR-11, FR-18, FR-19, NFR-2 | 2026-09-23 |
| ADR-0009 | JSON error bodies naming the failed component | accepted | FR-8, FR-17, FR-18, FR-20 | 2026-09-23 |
| ADR-0010 | One log line per wake execution and per error | accepted | FR-3, FR-9, FR-12, IF-6 | 2026-09-23 |
| ADR-0011 | Config file with environment-variable overrides | accepted | IF-5 | 2026-09-23 |
| ADR-0012 | Unbounded held set with a tested floor | accepted | FR-2, FR-20, NFR-4, NFR-5 | 2026-09-23 |
| ADR-0013 | TOML as the configuration format | accepted | IF-5 | 2026-09-23 |
| ADR-0014 | Error response body schema | accepted | FR-8, FR-17, FR-18, FR-20, NFR-6 | 2026-09-26 |
| ADR-0015 | Configuration file discovery and the `--config` flag | accepted | IF-5 | 2026-09-26 |
<!-- adr-index:end -->

## 10. Quality Requirements

> Coverage is not repeated here — the appendix owns it. This section holds the category and the scenario.

| ID | Requirement | Category |
|---|---|---|
| NFR-1 | First response within 120 s of a request received while hypha is not healthy | Performance efficiency |
| NFR-2 | First response within 2 s while hypha is healthy | Performance efficiency |
| NFR-3 | No more than 50 ms added latency per relayed chunk | Performance efficiency |
| NFR-4 | No read or write deadline on a held connection | Reliability |
| NFR-5 | At least 8 concurrent held requests, none discarded | Reliability |
| NFR-6 | Exactly one response per accepted request whose client stays connected | Reliability |
| NFR-7 | The wake command executes with the privileges it requires | Security |
| NFR-8 | Everything except the wake command runs without elevated privileges | Security |

### 10.1 Quality scenarios

- **NFR-1**: source a client agent · stimulus one request arrives bound for hypha · environment hypha powered off, the default 120 s bound · artifact the whole system · response the request is held, hypha is woken, the request is forwarded, hypha's response is returned · measure first response within 120 s of arrival on the reference network.
- **NFR-2**: source a client agent · stimulus one request arrives bound for hypha · environment hypha healthy, state already healthy · artifact the Listener and Forwarder · response the request is forwarded directly with no probe · measure first response within 2 s on the reference network.
- **NFR-3**: source hypha · stimulus a streamed response of 100 chunks, one per second · environment a request is being forwarded while the state is healthy · artifact the Forwarder's relay · response each chunk is relayed as it arrives · measure no more than 50 ms added per chunk on the reference network.
- **NFR-4**: source an auditor · stimulus inspection of the held connection's handling · environment a request held during a boot window · artifact the Listener and Intake · response no deadline is set on the connection · measure the absence of any read or write deadline in the connection-handling code.
- **NFR-5**: source a client agent population · stimulus 8 concurrent requests arrive during one boot window · environment hypha powered off · artifact the Held set · response all 8 are held and later forwarded · measure none is discarded and all 8 receive hypha's response.
- **NFR-6**: source the acceptance suite · stimulus every request the system accepts · environment the suite's full run · artifact the whole system · response exactly one response per request · measure the response count equals the accepted count minus the requests abandoned by their client.
- **NFR-7**: source an auditor · stimulus inspection of the wake command's file mode and owner, read from inside the running container · environment the system deployed · artifact the Wake trigger and the container specification · response the command carries its elevated bit and is invoked · measure the mode is 4755 and owned by root after deployment.
- **NFR-8**: source an auditor · stimulus inspection of the proxy process's uid and the container's capabilities · environment the system deployed · artifact the container specification · response the proxy runs unprivileged with no elevated capabilities · measure the process uid is not root and no elevated capability is granted.

## 11. Risks and Technical Debt

| Risk | From (ADR / requirement) | Impact | Mitigation |
|---|---|---|---|
| R-1 — the container runtime strips the setuid bit from the wake command, so the wake path fails while the host's file mode still looks correct | ADR-0003, ADR-0005, NFR-7 | Every request while hypha is off gets a 500 — the outage this project exists to prevent | Verify the bit from inside the running container at deployment, not on the host; make that part of the deployment check |
| R-2 — hypha's boot time grows beyond the ≈117 s of margin inside NFR-1's 120 s | ADR-0007, NFR-1, C-2 | The first response exceeds 120 s and the client gets a 504 | The bound is configuration, so it can be raised without a code change; if the boot time grows, NFR-1 needs revisiting |
| R-3 — hypha's idle-shutdown races the wake path, powering hypha off while requests are held | §2 accepted risk, non-goal 1 | Held requests wait for a boot that keeps being undone, then 504 | Accepted, not mitigated — the two hosts do not communicate, so the system cannot observe it |
| R-4 — a client stalls mid-body, so the request can never be forwarded and is held with no deadline | §2 non-goal 7, NFR-4, ADR-0004 | One held goroutine and its retained bytes are stuck indefinitely | Accepted: the spec puts connection timeouts out of scope. Detecting it would require a timeout, which NFR-4 forbids |
| R-5 — the held set is uncapped, so growth has no defined behaviour | ADR-0012, NFR-5 | Memory exhaustion rather than a clean refusal, if the client population grows or a client retries in a loop | The body cap bounds each request; the client population is a couple of agents. If it grows, ADR-0012 should be revisited |
| R-6 — a broken wake command fails every request immediately, which looks exactly like the outage this project prevents | FR-3, FR-17, ADR-0005 | Every hypha-bound request gets a 500 until the command is fixed | The 500 body names the wake command, so the cause is distinguishable from hypha being off; the deployment check in R-1 covers the common cause |
| R-7 — the listener is plain HTTP, so reaching it from outside hyperion would expose prompts in the clear | ADR-0006, §2 non-goal 6 | Request bodies, including prompts and tokens, readable on the network | The listen address must not be externally reachable; the system itself does not enforce this |
| R-8 — the first request after an out-of-band shutdown fails with a 502 | ADR-0008, FR-18, G1 | One error caused by hypha being off, which G1 otherwise excludes | Accepted in ADR-0008: the alternative, probing before every forward, adds a round trip to every healthy request and still does not close the window |
| R-9 — adding Go and a second HTTP implementation widens hyperion's maintenance surface | ADR-0001 | Another toolchain and HTTP stack to keep patched alongside the existing routing layer | Accepted: this is the cost of a runtime that makes hold, parallel release, and streaming structural; the nginx-module alternative was worse |
| R-10 — the hypha route is described in two places and can drift | ADR-0002 | A request for another service could arrive on the system's port and wake hypha for the wrong client | Keep the routing entry and the configured `listen_address` in step; the route is checked as part of the deployment verification |
| R-11 — the container runtime becomes a host dependency of a system whose job is to run when nothing else is | ADR-0003 | A misbehaving or stopped container runtime takes the wake path down | Accepted: everything on hyperion is deployed as a container; a systemd unit was rejected as the single operational exception |
| R-12 — a health endpoint slower than the 1 s probe timeout, or a state that flaps inside one 2 s interval, is misread | ADR-0007, IF-3 | Detection is delayed, or a change is missed until the next probe | The next probe corrects the state; the cadence and timeout are configuration and can be tightened |
| R-13 — the JSON error body is a client-facing contract | ADR-0009, ADR-0014 | A future field-name or shape change breaks the agents that branch on it | Treat the schema as stable; it is fixed in ADR-0014 and asserted by the four error-path tests |
| R-14 — logging only wake and error lines leaves no per-request trace | ADR-0010, §2 non-goal 3 | A fault inside a request cannot be diagnosed from the system's own logs | Accepted: logging bodies would write prompts and tokens to disk, which the spec forbids |
| R-15 — a configuration key can be set in both the file and the environment | ADR-0011 | A value that appears not to take effect has two candidate causes | Document the precedence (environment wins); an operator must read both sources to know the effective config |
| R-16 — the config file is the system's first third-party dependency, and durations and sizes are strings | ADR-0013 | A dependency to keep current; a typo in a duration or size is a start-time failure rather than a type error | The parser is small and stable; every value is validated at start with a message naming the offending key |
| R-17 — the config search order is a second place — code and document — that must agree | ADR-0015, IF-5 | A divergence changes which file the system reads, with no visible symptom until a value is wrong | The order is recorded in ADR-0015 and `config.example.toml`; a confirmation test asserts flag > environment > fixed default |

## 12. Glossary

| Term | Meaning here |
|---|---|
| healthy / not healthy | hypha's state as the system determines it, set only by a probe result (FR-10, FR-11, FR-19) |
| held request | a request accepted while hypha is not healthy and not yet answered |
| wait bound | the configured maximum time a request is held, measured from its arrival (FR-8) |
| held body cap | the configured maximum request body the system retains, 64 MiB by default (FR-20) |
| wake command | the configured setuid-root command the system executes to power hypha on (ADR-0005) |
| release | the moment the state becomes healthy and every held request is forwarded (FR-4, FR-5) |
| reference network | hyperion and hypha on the same LAN, with no other traffic |
| the system | tcp-wake: the reverse-proxy service on hyperion, not the routing layer, not hypha |

## Appendix: Requirement Coverage

> The single home for coverage.

| SRS ID | Addressed by (element / ADR) |
|---|---|
| FR-1 | Listener + Forwarder, ADR-0006 |
| FR-2 | Held set + Intake, ADR-0004 |
| FR-3 | Wake trigger, ADR-0005 |
| FR-4 | Forwarder release, ADR-0004 |
| FR-5 | Forwarder, one connection per held request, ADR-0001 |
| FR-6 | Held set, ADR-0004 |
| FR-7 | Held set discard on disconnect, ADR-0004 |
| FR-8 | Wait-bound timer, ADR-0009 |
| FR-9 | Routing boundary, ADR-0002 |
| FR-10 | Probe, ADR-0007 |
| FR-11 | Probe, ADR-0007 |
| FR-12 | Wake trigger, ADR-0002 + ADR-0010 |
| FR-13 | Intake + Forwarder verbatim, ADR-0004 |
| FR-14 | Forwarder relay, ADR-0004 |
| FR-15 | Forwarder streaming relay, ADR-0001 |
| FR-16 | In-memory state, §2 non-goal 5 |
| FR-17 | Wake trigger error path, ADR-0009 |
| FR-18 | Forwarder error path, ADR-0009 |
| FR-19 | Probe after transport failure, ADR-0008 |
| FR-20 | Intake body cap, ADR-0004 + ADR-0009 |
| IF-1 | Listener, ADR-0006 |
| IF-2 | Forwarder, ADR-0004 |
| IF-3 | Probe, ADR-0007 |
| IF-4 | Wake trigger, ADR-0005 |
| IF-5 | Config loader, ADR-0011 + ADR-0013 |
| IF-6 | Logger, ADR-0010 |
| NFR-1 | Held set + Probe + Wake trigger, ADR-0007 |
| NFR-2 | Listener + Forwarder direct path, ADR-0006 + ADR-0008 |
| NFR-3 | Forwarder streaming relay, ADR-0001 |
| NFR-4 | Listener + Intake, no deadline, ADR-0001 |
| NFR-5 | Held set, ADR-0012 |
| NFR-6 | Four error paths, ADR-0009 |
| NFR-7 | Wake trigger setuid boundary, ADR-0005 |
| NFR-8 | Container specification, ADR-0003 + ADR-0005 |
