# ADR-0007: Probe cadence 2 s interval with a 1 s timeout

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

The system must decide whether hypha is healthy (FR-10, FR-11) by probing its health endpoint (IF-3), and the first response for a request received while hypha is off must arrive within 120 s (NFR-1). The probe cadence sets how much of that budget is spent detecting that hypha has come up, and how much load the probe puts on a machine that is still booting. The recommendation put to the user was 1 s interval with a 1 s timeout; the user chose 2 s.

## Decision Drivers

- FR-10 — a ready status within the probe timeout means healthy
- FR-11 — anything else means not healthy
- IF-3 — ready is HTTP 200 with body `{"status":"ok"}` within the probe timeout
- NFR-1 — first response within 120 s of the request arriving
- C-2 — hypha's boot time is approximately 20 s
- C-5 — llama-server's router mode answers health before a model is loaded

## Considered Options

1. 1 s interval, 1 s timeout
2. 2 s interval, 1 s timeout
3. 500 ms interval, 500 ms timeout

## Decision Outcome

Chosen option: "2 s interval, 1 s timeout". The user chose this over the recommended 1 s interval to halve the probe rate against a host that is still booting. Worst-case detection after hypha starts answering is one interval plus one timeout, so approximately 3 s.

### Consequences

- Good: the probe asks a booting machine half as often, which matters because the probe runs alongside the boot rather than after it.
- Good: approximately 3 s of detection latency sits inside a 120 s budget with a wide margin, so the slower cadence costs nothing measurable against NFR-1.
- Bad: a health endpoint that takes longer than 1 s to answer is read as not healthy even though it is up, so detection waits for a faster answer. This is the spec's own definition (IF-3), not a defect, but the 1 s timeout is what makes it bite.
- Bad: because the cadence is slower than the recommendation, a hypha that becomes healthy and then unhealthy within a single 2 s window can be missed; the next probe corrects it.

## Pros and Cons of the Options

### 1 s interval, 1 s timeout

- Good, because detection is bounded at approximately 2 s, the tightest of the three inside the budget.
- Bad, because it probes a booting machine twice as often for a saving the budget does not need.

### 2 s interval, 1 s timeout

- Good, because it halves the probe rate while keeping detection well inside NFR-1.
- Good, because the timeout is unchanged, so a slow health endpoint is treated the same way as under option 1.
- Bad, because worst-case detection is approximately 3 s rather than 2 s.

### 500 ms interval, 500 ms timeout

- Good, because detection would be fastest.
- Bad, because it adds load exactly when hypha is least able to absorb it.
- Bad, because a 500 ms timeout would read a healthy but momentarily busy endpoint as not healthy, causing the state to flap during boot.

## Confirmation

Test: with hypha off and then answering, the recorded state becomes healthy within 5 s of the health endpoint's first ready answer, and the held request's first response arrives within NFR-1's 120 s.

## Links

- Requirements: FR-10, FR-11, IF-3, NFR-1, C-2, C-5
- Related ADRs: ADR-0008 (when the probe runs), ADR-0011 (where the cadence and timeout are configured)
- Architecture document: §6 Runtime View, §10 Quality Requirements
