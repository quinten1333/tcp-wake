# ADR-0012: Unbounded held set with a tested floor

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

NFR-5 requires at least 8 concurrent requests to be held without discarding any, and the body cap (FR-20) already bounds the size of a single held request. Whether the number of held requests is itself capped decides whether there is a fifth error path — a request refused because the system is already holding too many.

## Decision Drivers

- NFR-5 — hold at least 8 concurrent requests without discarding any
- FR-2 — hold a request received while hypha is not healthy
- FR-20 — the body cap bounds what one held request may retain
- §2 non-goal 5 — held requests are not preserved across a restart

## Considered Options

1. Unbounded held set, with NFR-5's 8 as the tested floor
2. Capped held set, returning an error above the cap
3. Capped held set, blocking new accepts until space frees

## Decision Outcome

Chosen option: "Unbounded held set, with NFR-5's 8 as the tested floor", because the clients are a couple of agents rather than a public audience, so the count that matters is small and known, and a cap would introduce a fifth error path whose body would have to name yet another limit.

### Consequences

- Good: there is no path on which a request is refused for a reason unrelated to hypha, so G1 has one fewer exception.
- Good: worst-case memory is bounded already — the body cap multiplied by the number of requests actually held — so "unbounded" is bounded in practice by the client population.
- Bad: nothing in the system stops the held set from growing if the client population grows or a client retries in a loop, and the failure mode would be memory exhaustion rather than a clean refusal.
- Bad: NFR-5's floor of 8 is a test, not a limit, so the system's real capacity is unstated and unmeasured.

## Pros and Cons of the Options

### Unbounded held set, with NFR-5's 8 as the tested floor

- Good, because it removes an error path that would otherwise compete with the four the spec defines.
- Good, because it matches the deployment's actual client count.
- Bad, because growth has no defined behaviour.

### Capped held set, returning an error above the cap

- Good, because the failure mode would be a clean refusal rather than exhaustion.
- Bad, because the cap's error is a fifth condition, and G1 is defined as no client seeing an error caused by hypha being off — a capacity refusal during a wake is exactly that.
- Bad, because the cap would have to be tuned against a client count the user describes as a couple of agents, so it would be set arbitrarily.

### Capped held set, blocking new accepts until space frees

- Good, because no request would be refused and memory would stay bounded.
- Bad, because a blocked accept is invisible to the client and indistinguishable from the hold the system already performs, so the two would be impossible to tell apart when diagnosing a slow response.
- Bad, because it moves the capacity question into the listener, where it interacts with the absence of timeouts (NFR-4) in a way nothing specifies.

## Confirmation

Test: 8 concurrent requests during one boot window all receive hypha's response (NFR-5). Inspection: no capacity check exists in the accept path.

## Links

- Requirements: NFR-5, FR-2, FR-20, §2 non-goal 5
- Related ADRs: ADR-0004 (the cap that bounds each request), ADR-0001 (the runtime whose memory this is)
- Architecture document: §11 Risks
