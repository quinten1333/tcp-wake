# ADR-0008: Trust the cached health state, and probe only after a transport failure

```yaml
---
status: accepted
date: 2026-09-23
decision-makers: [the user]
consulted: []
informed: []
---
```

## Context and Problem Statement

While the system believes hypha is healthy it forwards directly (FR-1), and a request forwarded while hypha is not healthy must not produce an error caused by hypha being off (G1). But hypha can be powered off out-of-band, since hyperion does not control its shutdown (non-goal 1) and the two hosts do not otherwise communicate. The system must decide whether to verify its belief before each forward, or to act on it and correct it when a forward fails (FR-18, FR-19).

## Decision Drivers

- FR-1 — forward and return hypha's response while hypha is healthy
- FR-18 — a transport-level forward failure returns an error for that request
- FR-19 — a transport-level forward failure triggers a health probe whose result sets the state
- NFR-2 — first response within 2 s while hypha is healthy
- §2 accepted risk — the two hosts do not communicate outside the request path

## Considered Options

1. Trust the cached state; let FR-18 and FR-19 catch a failure
2. Probe before every forward while believing hypha healthy
3. Probe on a timer while believing hypha healthy

## Decision Outcome

Chosen option: "Trust the cached state; let FR-18 and FR-19 catch a failure", because FR-19 already specifies the correction path: when a forward fails at the transport level, the system probes and sets the state from the probe's result, so the belief is repaired by evidence rather than by prediction.

### Consequences

- Good: NFR-2's 2 s budget is spent on the request alone, with no probe round trip added to every healthy request.
- Good: the state is only ever changed by an observation — a probe result (FR-10, FR-11) or a probe after a failure (FR-19) — so there is no inference from failure to cause, which is what the user asked for.
- Bad: the first request after an out-of-band shutdown fails with a 502 (FR-18). That error is caused by hypha being off, which is the one case where G1 is not met, and it is the price of not probing before every forward.
- Bad: the failure and the probe are two steps, so a request arriving between them also fails.

## Pros and Cons of the Options

### Trust the cached state; let FR-18 and FR-19 catch a failure

- Good, because it adds nothing to the healthy path, where the system must be invisible (G2).
- Good, because the correction is specified by a requirement rather than left to the implementation.
- Bad, because one request per out-of-band shutdown fails.

### Probe before every forward while believing hypha healthy

- Good, because the window in which the belief is stale would shrink to one probe interval.
- Bad, because it does not close the window — hypha can still be powered off between the probe and the forward — so it buys a smaller failure rate, not the absence of one.
- Bad, because it adds a probe round trip to every healthy request, which is exactly where the system must be invisible.

### Probe on a timer while believing hypha healthy

- Good, because it would detect an out-of-band shutdown without waiting for a request to fail.
- Bad, because a timer keeps probing a machine that is deliberately off (G4), and the spec's non-goal 5 means the probe would run for no pending request.
- Bad, because it makes the state depend on a background process rather than on the requests the system exists to serve.

## Confirmation

Test: with hypha powered off after the system last saw it healthy, one request receives a 502 and the recorded state afterwards matches a probe's result. Test: the next request after that behaves according to the state — held and woken if not healthy, forwarded if healthy.

## Links

- Requirements: FR-1, FR-18, FR-19, NFR-2, §2 accepted risk
- Related ADRs: ADR-0007 (the probe parameters this uses), ADR-0009 (the error shape returned)
- Architecture document: §6 Runtime View, §11 Risks
