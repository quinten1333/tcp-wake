# Requirements Specification — tcp-wake

> Subject in every requirement is **the system**: the wake-on-demand reverse proxy on hyperion.
> Normative keyword: `shall` marks a requirement. `will` states a fact. `should` marks an uncommitted goal.
> **Baseline v0.4** — reviewed and confirmed by the user.
> Change control: any change to this document after baseline requires an impact assessment and a re-run of the pitfalls checklist on the changed requirement and its trace links.
>
> **Change log** — v0.2 (2026-10-08, ADR-0016): the wake mechanism is fixed to
> `etherwake` with a `CAP_NET_RAW` file capability, so the configurable wake
> command is replaced by `wake_mac` (required) and `wake_interface` (optional;
> unset omits `-i` and uses etherwake's own default). Affected: §1, §2 non-goal 4, Terms, FR-3, FR-17, NFR-7, IF-4, IF-5,
> §5.4, §5.5. Impact: NFR-7's check becomes a file capability instead of a
> setuid file mode; the system is no longer pluggable to a non-`etherwake` wake
> command; R-1 changes shape but remains (see `docs/architecture.md` §11).
>
> **Change log** — v0.3 (2026-10-08, ADR-0017): the log interface (§5.6) now also
> carries one line per target health-state transition. No requirement changes:
> IF-6 still requires one line per wake execution, and FR-3/FR-9/FR-12 still
> count wake executions. The new line is an addition to the interface, not a new
> required behaviour.
>
> **Change log** — v0.4 (2026-10-08, ADR-0018): the request path now tries the
> target first, with the upstream connect bounded by the probe timeout, and only
> holds and wakes when that connection cannot be established. FR-18 is narrowed
> to a transport failure *after* the connection is established; an unreachable
> target is held and woken, removing the accepted risk R-8. §5.1 and §5.2 are
> amended. No other requirement changes.

## 1. Goal and Context

- **Goal (one sentence)**: The system shall deliver every client request bound for hypha to hypha — waking hypha when it is off and holding the request until hypha is healthy — so that no client sees an error caused by hypha being off.
- **What it is, in one paragraph**: A reverse-proxy service on the always-on host **hyperion**, sitting in front of **hypha**, an LLM host that is normally powered off to save electricity. A request arriving while hypha is off is held open with no error response, `etherwake` runs, hypha's health endpoint is polled, and every held request is forwarded as soon as hypha reports healthy.
- **Who or what uses it**: Any HTTP client that would otherwise address hypha directly — agents, editors, chat frontends, scripts. They send ordinary requests at arbitrary times and are not adapted to hypha being off.
- **How you will know it works**: A client sends a request while hypha is off and receives hypha's real response after a long wait, with no error status. Repeated requests while hypha is healthy are indistinguishable from talking to hypha directly.

**Goal labels used in the tables below**

| Label | Goal |
|---|---|
| G1 | No client sees an error caused by hypha being off |
| G2 | The system is invisible to clients while hypha is healthy |
| G3 | Hyperion can wake hypha but cannot log into it |
| G4 | Hypha stays off while no request is pending |

## 2. Scope and Constraints

- **In scope**: the hypha-bound request path — hold, wake trigger, health detection, release, forwarding, and the errors returned when the wake command fails, when hypha never becomes healthy, when a forward fails at the transport level, and when a request body exceeds the held body cap.
- **Out of scope / non-goals**:
  1. Powering hypha off. Hyperion may wake hypha and must not be able to log into it, so shutdown is owned by hypha itself (an existing idle-shutdown mechanism on hypha).
  2. Waiting for a model to load. llama-server runs in router mode and owns model loading; hypha is healthy, and requests are forwarded, before any model is resident.
  3. Inspecting, transforming, caching, or storing request or response content.
  4. Waking hypha by any mechanism other than the fixed `etherwake` invocation.
  5. Holding requests across a restart of the system.
  6. Authenticating clients. The pre-existing routing on hyperion is the trust boundary.
  7. Timing out idle, stalled, or slow client connections. The components on either side — llama.cpp and hyperion's routing layer — own connection timeouts; the system applies none of its own to a held request.
- **Constraints**:
  - **C-1**: hyperion is always on and is the only always-on host in this system; hypha is normally off.
  - **C-2**: hypha's boot time is approximately 20 s, so the default wait bound must cover it with margin.
  - **C-3**: hyperion has no login path to hypha. No credential for hypha exists on hyperion.
  - **C-4**: the wake command requires elevated privileges on hyperion; the system's other parts do not.
  - **C-5**: hypha runs llama-server in router mode, whose health endpoint reports ready before any model is loaded.
- **Accepted risk**: hyperion and hypha do not communicate outside the request path, so hypha's own idle-shutdown could in principle power hypha off while requests are held. The system cannot observe this and does not try to prevent it. Accepted, not mitigated.
- **Terms**: *healthy* and *not healthy* are hypha's states as the system determines them (§5.3). *Held request* is a request the system has accepted and not yet answered. *Wake command* is the fixed `etherwake` invocation the system executes to power hypha on. *Wait bound* is the configured maximum time the system holds a request. *Held body cap* is the configured maximum request body the system retains. *Reference network* is hyperion and hypha on the same LAN, with no other traffic.
- **Assumptions** (facts the requirements depend on):
  - hypha's health endpoint is `GET /health`, returning HTTP 200 with body `{"status":"ok"}` when ready and HTTP 503 while a model is loading. In router mode the ready answer does not depend on a loaded model.
  - hypha's boot time of approximately 20 s (C-2) is carried from the previous specification of this system and is not re-measured here.
  - hyperion already routes client traffic for hypha and can route it to a dedicated listener.

## 3. Functional Requirements

| ID | Requirement | Check | Goal |
|---|---|---|---|
| FR-1 | When the system receives a request bound for hypha while hypha is healthy, the system shall forward the request to hypha and return hypha's response to the client. | test: with hypha healthy, 100 sequential requests each return hypha's exact status and body | G2 |
| FR-2 | When the system receives a request bound for hypha while hypha is not healthy, the system shall hold the request open without sending a response. | test: with hypha off, one request returns no bytes before hypha reports healthy | G1 |
| FR-3 | When the system receives a request bound for hypha while hypha is not healthy, the system shall execute the wake command once for that request. | test: with hypha off, 5 requests during one boot window produce 5 wake-command executions | G1 |
| FR-4 | When hypha becomes healthy, the system shall forward every held request to hypha. | test: with 3 requests held during boot, all 3 are forwarded and all 3 receive hypha's response | G1 |
| FR-5 | When hypha becomes healthy, the system shall begin forwarding each held request without waiting for any other held request to finish. | test: with 3 requests held, all 3 upstream connections open within 100 ms of each other | G1 |
| FR-6 | While the system holds a request, the system shall send no response bytes to the client for that request. | test: hold a request and assert the client receives zero bytes until hypha is healthy | G1 |
| FR-7 | When a client closes the connection for a held request, the system shall discard that request. | test: close the client connection while held, then assert hypha's access log shows no forward for it | G1 |
| FR-8 | If hypha does not become healthy within the configured wait bound measured from the request's arrival, then the system shall return an error response to the client naming the wait bound. | test: with the wake command succeeding but hypha never answering, the client receives an error naming the bound 120 s after the request arrived | G1 |
| FR-9 | When the system receives a request bound for a service other than hypha, the system shall not execute the wake command. | test: 60 s of traffic to another vhost produces zero wake-command executions | G4 |
| FR-10 | When hypha's health endpoint returns a ready status within the probe timeout, the system shall treat hypha as healthy. | test: with hypha up, the probe succeeds and the state becomes healthy | G1 |
| FR-11 | When hypha's health endpoint does not return a ready status within the probe timeout, the system shall treat hypha as not healthy. | test: with hypha down, the probe times out and the state stays not healthy | G1 |
| FR-12 | While no request bound for hypha is pending, the system shall not execute the wake command. | test: 10 min of no requests produces zero wake-command executions | G4 |
| FR-13 | When the system forwards a request to hypha, the system shall transmit the request's method, path, headers, and body unchanged. | test: compare the request seen in hypha's access log with the client's original for 10 held requests | G2 |
| FR-14 | When the system receives a response from hypha, the system shall transmit the response's status, headers, and body to the client unchanged. | test: compare status, headers, and body at the client against hypha's response for 10 requests | G2 |
| FR-15 | When hypha's response is streamed, the system shall relay each chunk to the client as the chunk arrives. | test: a stream of 100 chunks emitted 1 s apart reaches the client without batching | G2 |
| FR-16 | When the system restarts while requests are held, the system shall not forward those requests to hypha. | test: restart mid-hold, then assert hypha's access log shows no forward for the held requests | G1 |
| FR-17 | If the wake command exits with a non-zero status, then the system shall return an error response to that request's client immediately, naming the wake command as the failed component. | test: set an invalid wake interface so `etherwake` exits non-zero, and assert the error body names the wake command | G1 |
| FR-18 | If a forward to hypha fails at the transport level after the connection is established, then the system shall return an error response to that request's client. | test: have hypha accept the connection then drop it, and assert the client receives an error | G1 |
| FR-19 | If a forward to hypha fails at the transport level, then the system shall probe hypha's health endpoint and set hypha's state from that probe's result. | test: after a transport-level forward failure, the recorded state matches the probe's result and the next request behaves accordingly | G1 |
| FR-20 | If a request body exceeds the configured held body cap, then the system shall return an error response naming the cap. | test: send a body above the configured cap and assert the error names it | G1 |

## 4. Non-Functional Requirements

| ID | Requirement | Check | Goal |
|---|---|---|---|
| NFR-1 | The system shall return the first response for a request received while hypha is not healthy within 120 s of receiving that request on the reference network. | test: with hypha off and the default bound, first-response time is at most 120 s | G1 |
| NFR-2 | The system shall return the first response for a request received while hypha is healthy within 2 s of receiving that request on the reference network. | test: with hypha healthy, 100 requests each return a first response within 2 s | G2 |
| NFR-3 | The system shall add no more than 50 ms of latency to each streamed chunk it relays on the reference network. | test: compare hypha's chunk timestamps with the client's over a 100-chunk stream | G2 |
| NFR-4 | While the system holds a request, the system shall apply no read or write deadline to that request's connection. | inspection: no deadline is set on held connections in the connection-handling code | G1 |
| NFR-5 | The system shall hold at least 8 concurrent requests bound for hypha without discarding any. | test: 8 concurrent requests — a couple of agents issuing a few requests each — during one boot window all receive hypha's response | G1 |
| NFR-6 | The system shall return exactly one response for every accepted request whose client connection stays open. | test: across the acceptance suite, the response count equals the accepted request count minus the requests abandoned by their client | G1 |
| NFR-7 | The system shall execute the wake command with the privileges that command requires. | inspection: `etherwake` carries `cap_net_raw` and the unprivileged proxy user runs it successfully | G3 |
| NFR-8 | The system shall run every part of the system other than the wake command without elevated privileges. | inspection: the proxy process uid, and the absence of elevated capabilities in its deployment specification | G3 |

## 5. Interfaces

| ID | Interface | Check | Goal |
|---|---|---|---|
| IF-1 | The system shall accept a request from a client over HTTP and return hypha's response over the same connection. | test: send a request and assert the response status and body shape | G2 |
| IF-2 | The system shall forward a held request to hypha over HTTP and relay hypha's response to the client. | test: compare the forwarded request's method, path, headers, and body with the client's | G1 |
| IF-3 | The system shall treat hypha as healthy only when hypha's health endpoint returns HTTP 200 with body `{"status":"ok"}` within the probe timeout. | test: call the health endpoint in each state and assert the resulting state transition | G1 |
| IF-4 | The system shall execute the wake command as a child process and read its exit status. | test: with an injected wake command, assert one execution and the observed exit status | G1 |
| IF-5 | The system shall read the wait bound, hypha's MAC address, the wake interface, hypha's address, the health endpoint path, the probe cadence, the probe timeout, and the held body cap from a configuration file. | test: change each key and assert the changed behaviour | G1 |
| IF-6 | The system shall write one log line per wake-command execution. | test: count log lines against the number of triggering requests | G1 |

### 5.1 Client interface (client ↔ system)

- **Ends**: any HTTP client ↔ the system's listener on hyperion.
- **Crosses**: the client's request bytes inbound; hypha's response bytes outbound, including streamed chunks.
- **Format**: HTTP over TCP. Method, path, headers, and body pass through unmodified in both directions (FR-13, FR-14).
- **Failure**: while a request is held, no bytes are sent and no status is emitted (FR-2, FR-6). Four error paths exist, and each body names the component or limit responsible:

  | Condition | Status | Body names |
  |---|---|---|
  | The wait bound elapses (FR-8) | 504 | the wait bound |
  | The wake command exits non-zero (FR-17) | 500 | the wake command |
  | A forward fails at the transport level after the connection is established (FR-18) | 502 | hypha as unreachable |
  | A request body exceeds the held body cap (FR-20) | 413 | the held body cap |

  The wait bound is measured from the request's arrival and is not restarted by a wake-command execution (FR-8), so a request's total wait cannot exceed the bound.
  A client that closes the connection while held loses that request only (FR-7). The system applies no connection timeout of its own (§2 non-goal 7, NFR-4).

### 5.2 Origin interface (system ↔ hypha)

- **Ends**: the system ↔ hypha's HTTP listener.
- **Crosses**: the forwarded request bytes outbound; hypha's response bytes inbound.
- **Format**: HTTP over TCP, one upstream connection per held request, opened in parallel across held requests (FR-5). Streamed responses are relayed chunk by chunk (FR-15).
- **Failure**: hypha's own error responses are relayed unchanged as responses (FR-14). A failure to establish the connection means hypha is off: that request is held, the wake command runs, and it is forwarded once hypha is ready (ADR-0018). A transport-level failure *after* the connection is established produces an error for that request's client (FR-18) and triggers an immediate health probe whose result sets hypha's state (FR-19).

### 5.3 Health interface (system ↔ hypha)

- **Ends**: the system's health probe ↔ hypha's `GET /health`.
- **Crosses**: a probe request outbound; a status and body inbound.
- **Format**: HTTP. Ready is HTTP 200 with body `{"status":"ok"}`; HTTP 503 with body `{"error":{"message":"Loading model"}}` means not ready. Any other status, and any probe exceeding the probe timeout, means not healthy (IF-3, FR-10, FR-11).
- **Failure**: the probe runs while a request is pending and once on each transport-level forward failure (FR-19). It does not run on a timer while idle, which is the probe-side counterpart of FR-12 and serves G4. Probe failures are not surfaced to clients; they only set the state to not healthy.

### 5.4 Wake interface (system ↔ etherwake)

- **Ends**: the system (unprivileged) ↔ the fixed `etherwake` binary on hyperion (requires `CAP_NET_RAW`).
- **Crosses**: one process invocation per triggering request; the command's exit status back to the system.
- **Format**: the command is fixed to `etherwake`; the target MAC address and the wake interface are configuration (IF-5).
- **Failure**: a non-zero exit is observable to the system and produces an immediate error for that request's client (FR-17). Because each request triggers its own execution (FR-3), a broken command fails each request on its own execution rather than through any shared state.

### 5.5 Configuration interface

- **Ends**: the system ↔ its configuration file.
- **Crosses**: the wait bound (default 120 s), hypha's MAC address, the wake interface (optional), hypha's address, the health endpoint path, the probe cadence, the probe timeout, the held body cap (default 64 MiB), and the listen address.
- **Format**: one file with named keys (IF-5).
- **Failure**: a missing or unreadable configuration file prevents the system from starting, and the failure is reported at start.

### 5.6 Log interface

- **Ends**: the system ↔ its log output on hyperion.
- **Crosses**: one line per wake-command execution, carrying a timestamp and the exit status (IF-6); one line per error; and one line per target health-state transition.
- **Format**: greppable text, one line per event: `wake command=… status=…`, `error status=… component=… message=…`, and `health state=healthy|unhealthy`.
- **Failure**: this log is the counting artifact for FR-3, FR-9, and FR-12, so an unwritable log is a start-time failure.

## 6. Open Questions

None. Every question raised while drafting this baseline is resolved and folded into the sections above.
