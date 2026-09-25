# ADR-0010: One log line per wake execution and per error

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

FR-3, FR-9, and FR-12 are all counted in wake-command executions, and IF-6 requires one log line per execution so that count can be read from outside the system. The system also produces four error paths that have to be diagnosable after the fact. What gets logged decides both whether the requirements are checkable and whether request content ends up on disk.

## Decision Drivers

- IF-6 — one log line per wake-command execution
- FR-3 — one execution per triggering request, counted
- FR-9 — no execution for a request bound for another service, counted
- FR-12 — no execution while no request is pending, counted
- §2 non-goal 3 — no inspection, transformation, or storage of request or response content

## Considered Options

1. One line per wake execution plus one per error, as text
2. Structured JSON to the system journal
3. Full request logging

## Decision Outcome

Chosen option: "One line per wake execution plus one per error, as text", because the wake lines are a counting artefact the requirements are verified against, and text makes them greppable with no tooling between the log and the count.

### Consequences

- Good: FR-3, FR-9, and FR-12 are checkable by counting lines, which is exactly what IF-6 asks for.
- Good: the four error paths leave a record, so a client's report of a failure can be matched against the system's own view of it.
- Bad: no request or response content is logged, so a fault inside a request body cannot be diagnosed from the system's logs alone; it has to be reproduced.
- Bad: the log carries no state transitions, so a "why did it not wake" question needs the wake lines plus the client's own timing rather than one trace.

## Pros and Cons of the Options

### One line per wake execution plus one per error, as text

- Good, because the count and the log line are the same thing, so the requirement and its check cannot drift.
- Good, because a text line survives any log pipeline, including the container runtime's default output.
- Bad, because there is no per-request trace, by design.

### Structured JSON to the system journal

- Good, because a field-based query could answer questions the text form cannot.
- Bad, because it makes the count depend on the journal's availability, so IF-6's check would need the journal to be working as well as the system.
- Bad, because it is more format to maintain than the requirements justify.

### Full request logging

- Good, because a fault in a request would be diagnosable from the log.
- Bad, because it writes prompts and tokens to disk, which no requirement asks for and which is a privacy cost on a shared host.
- Bad, because it grows without bound while a request is held, which is the state the system spends most of its interesting time in.

## Confirmation

Test: the number of wake log lines equals the number of triggering requests, and zero lines appear for traffic to another vhost or for a period with no requests. Inspection: no request or response content appears in the log.

## Links

- Requirements: IF-6, FR-3, FR-9, FR-12, §2 non-goal 3
- Related ADRs: ADR-0009 (the error paths that are logged), ADR-0005 (the command whose execution is counted)
- Architecture document: §8 Cross-cutting Concepts (logging)
