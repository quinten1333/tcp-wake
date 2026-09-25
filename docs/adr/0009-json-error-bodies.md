# ADR-0009: JSON error bodies naming the failed component

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

The spec defines four error paths, and the user requires that the body make clear which component has failed. The clients are agents and editors rather than people reading a page, so the shape of the body decides whether a failure can be handled automatically or only reported.

## Decision Drivers

- FR-8 — the wait bound elapsing returns an error naming the wait bound
- FR-17 — a non-zero wake-command exit returns an error naming the wake command
- FR-18 — a transport-level forward failure returns an error
- FR-20 — a body above the cap returns an error naming the cap
- §5.1 — each error body names the component or limit responsible

## Considered Options

1. JSON body naming the component and the limit
2. Plain-text body naming the component
3. Status code only

## Decision Outcome

Chosen option: "JSON body naming the component and the limit", because the clients are programs, and a named field they can branch on is what turns "hypha did not come up" into a retry decision rather than a message someone has to read.

### Consequences

- Good: an agent can distinguish the four conditions without parsing prose, which is what the user asked for when they required the reason to be in the body.
- Good: the four statuses stay stable and meaningful to ordinary HTTP tooling, while the body carries the detail those statuses cannot express.
- Bad: the body is a format the system must keep stable, so a future change to its field names is a breaking change for clients.
- Bad: the system now has an output format to document and to test, where a status code alone would have needed neither.

## Pros and Cons of the Options

### JSON body naming the component and the limit

- Good, because it is machine-readable, and the clients are machines.
- Good, because the limit — the wait bound or the body cap — can be carried as a value, so a client can adapt rather than only report.
- Bad, because it is a contract that must be maintained.

### Plain-text body naming the component

- Good, because it is readable by a person in a terminal without tooling.
- Bad, because a program must pattern-match prose to decide anything, which is brittle.
- Bad, because a limit is harder to carry as a value inside a sentence.

### Status code only

- Good, because it is the least to maintain.
- Bad, because it directly contradicts the user's requirement that the reason be in the body.
- Bad, because 502 for a transport failure and 500 for a wake-command failure would leave a client unable to tell which of the two components to blame.

## Confirmation

Test: each of the four conditions is triggered and its response body is asserted to name the expected component or limit, and to carry the expected status. The four are 504 for the wait bound, 500 for the wake command, 502 for a transport-level forward failure, and 413 for a body above the cap.

## Links

- Requirements: FR-8, FR-17, FR-18, FR-20, §5.1
- Related ADRs: ADR-0008 (the transport failure this reports), ADR-0004 (the cap this reports)
- Architecture document: §5 Building Block View, §8 Cross-cutting Concepts (error handling)
