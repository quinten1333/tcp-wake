# ADR-0018: Try the target before waking

```yaml
---
status: accepted
date: 2026-10-08
decision-makers: [the user]
consulted: []
informed: []
---
```

## Context and Problem Statement

ADR-0008 makes the request path trust the cached health belief: a request that
arrives while the belief is not healthy wakes the target immediately. Because a
fresh process starts not healthy (FR-16/§6.8), the first request after a restart
runs the wake command even when hypha is already on. ADR-0008 also accepted R-8:
the first request after an out-of-band shutdown gets a 502. The user asked for
the system to assume the target is up — try to connect and forward first, with
the connect bounded by the short probe timeout, and only mark the target
unhealthy, hold, and wake when that connection cannot be established.

## Decision Drivers

- G1 — no client sees an error caused by hypha being off
- FR-1, FR-2, FR-3 — forward when healthy, hold and wake when not
- FR-8 — the wait bound is measured from arrival and never restarted
- FR-18, FR-19 — the transport-failure error and the corrective probe
- NFR-1, NFR-2 — first response within the bound / within 2 s while healthy
- R-8 — the accepted first-request-after-shutdown 502 this decision removes

## Considered Options

1. Keep ADR-0008: trust the cached belief and wake when it says not healthy.
2. Try the target first, and only wake when the connect fails (chosen).
3. Probe `GET /health` before waking, instead of trying the request itself.

## Decision Outcome

Chosen option: "Try the target first". `Pipeline.Handle` calls the forwarder
before consulting the belief. The forwarder's dial is bounded by the configured
`probe_timeout`; a dial failure returns an error wrapping `ErrTargetUnreachable`
before any client byte is written. Only then does the pipeline record the target
unhealthy (`Prober.Observe(false)`), run the wake command once for that request
(FR-3), and hold under the wait bound. A failure after the connection is
established is the FR-18 502 and triggers `ProbeNow` (FR-19). A successful
forward records the target healthy (`Prober.Observe(true)`), so a stale
not-healthy belief is corrected by the request path itself.

The health belief is therefore written by the request path's own attempt as well
as by the probe loop. `Prober.Observe` is the shared seam, so a transition is
logged once (ADR-0017) whether it came from an attempt or a probe.

This supersedes ADR-0008. ADR-0007 (the probe cadence) still governs the probe
loop that releases held requests.

### Consequences

- Good: no wake runs when the target is already up; the first request after a
  restart is forwarded directly.
- Good: R-8 is removed. A target that goes off out-of-band is held and woken
  instead of getting a 502, so G1 holds for that case too.
- Good: a stale not-healthy belief self-corrects on the next request, because
  the request path observes the target directly.
- Bad: the upstream connect now has a timeout. §8.5's "no connection timeout
  anywhere" is amended; the held client connection still gets no deadline of any
  kind (NFR-4), and this is a connect timeout on the system's own upstream, not
  on a client (§2 non-goal 7).
- Bad: FR-18 narrows to a failure *after* the connection is established, and
  FR-19's corrective probe applies to that failure and to the wake path. The SRS
  is amended accordingly.
- Bad: readiness and reachability are now one signal. This holds for this
  deployment because hypha runs llama-server in router mode, where the listener
  answers ready once it accepts (C-5); a target that accepts but is not ready
  would have its response relayed unchanged rather than held.

## Pros and Cons of the Options

### Keep ADR-0008 (trust the cached belief)

- Good, because the belief is simple to reason about and the healthy path adds
  no extra work.
- Bad, because a fresh process starts not healthy and wakes an already-on
  target on its first request.
- Bad, because the first request after an out-of-band shutdown is a 502 (R-8).

### Try the target first, wake on a failed connect

- Good, because the target's own answer, not a cached belief, decides the path.
- Good, because an already-on target is never woken and an off target still
  wakes, holds, and forwards.
- Bad, because it adds a bounded connect attempt (probe_timeout) to every
  request that arrives while the belief is not healthy.
- Bad, because a request is sent before the system knows whether the target is
  ready; for a target that accepts but is not ready, its response is relayed
  rather than held.

### Probe `/health` before waking

- Good, because it keeps the readiness signal distinct from reachability and
  would not relay a not-ready target's response.
- Bad, because it adds a health round trip to the not-healthy path and keeps two
  notions of "is it up" instead of one.

## Confirmation

`TestADR0018ReachableTargetForwardsWithoutWaking` (no wake when the target is
up), `TestADR0018UnreachableTargetHeldAndWoken` (hold, one wake, forward), and
`TestADR0018OutOfBandShutdownWakesInsteadOf502` (the removed R-8). Existing
`TestFR18TransportFailureGives502` now uses a target that accepts then closes,
and `TestForwardNoClientDeadlineInSource` keeps client deadlines out of the
forward path while allowing the upstream dial timeout.

## Links

- Requirements: FR-1, FR-2, FR-3, FR-8, FR-18, FR-19, NFR-1, NFR-2, R-8
- Related ADRs: ADR-0008 (superseded), ADR-0007 (the probe cadence), ADR-0017
  (the transition line the attempt now writes)
- Architecture document: §5.2, §6, §8.5, §11 R-8
