# ADR-0002: Dedicated listen address behind the existing routing

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

FR-9 requires that a request bound for a service other than hypha never triggers the wake command, and the spec puts client authentication outside this system, making hyperion's existing routing the trust boundary. Where the system sits relative to that routing decides whether both hold, and whether the system can be deployed without touching how other services are served.

## Decision Drivers

- FR-9 — a request for another service shall not execute the wake command
- FR-1 — forward and return hypha's response while hypha is healthy
- §2 non-goal 6 — client authentication is not this system's job
- C-3 — hyperion has no login path to hypha

## Considered Options

1. Dedicated listen address behind the existing routing
2. Replace the existing proxy
3. A module inside the existing proxy

## Decision Outcome

Chosen option: "Dedicated listen address behind the existing routing", because the existing routing already terminates client traffic and decides which service a request is for; the system takes one route bound for hypha and leaves every other vhost exactly as it is.

### Consequences

- Good: FR-9 is satisfied by construction rather than by a check — a request that is not routed to the system's address never reaches the code that can wake hypha.
- Good: the system can be deployed, restarted, and removed without editing how any other service is served.
- Bad: two places now describe the same route — the existing routing entry and the system's configured listen address. If they drift, a request intended for another service can arrive on the system's port and be treated as hypha-bound, which would wake hypha for the wrong client.

## Pros and Cons of the Options

### Dedicated listen address behind the existing routing

- Good, because it keeps the trust boundary where the spec put it.
- Good, because the blast radius of a bug is one route, not all of them.
- Bad, because the route is described in two files that can disagree.

### Replace the existing proxy

- Good, because there would be one routing configuration instead of two.
- Bad, because it requires reimplementing routing the system does not own, for services that have nothing to do with hypha — scope no requirement asks for.
- Bad, because a fault in the new routing would take down every service on hyperion, not just hypha-bound traffic.

### A module inside the existing proxy

- Good, because the hold state would share the existing routing's process and need no second listener.
- Bad, because the hold state would then be inside a process that reloads and restarts for unrelated reasons, while §2 non-goal 5 says held requests are not preserved across a restart.
- Bad, because the module's lifecycle would be tied to the proxy's configuration model rather than to the system's own.

## Confirmation

Inspection: one routing entry bound for hypha pointing at the system's configured listen address, and no other entry changed. FR-9's test is 60 s of traffic to another vhost producing zero wake-command executions.

## Links

- Requirements: FR-1, FR-9, §2 non-goal 6, C-3
- Related ADRs: ADR-0006 (the listen surface), ADR-0011 (where the listen address is configured)
- Architecture document: §3 Context and Scope, §7 Deployment View
