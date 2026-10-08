# ADR-0017: Health-state-change log lines

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

ADR-0010 fixed the log to one line per wake-command execution and one per error.
The user asked for the target's health transitions to be logged as well, so an
operator can see when hypha becomes reachable or unreachable without counting
probes or inferring it from a wake or error line. FR-10 and FR-11 define the two
states and IF-3 fixes what makes a probe ready; §5.6 and IF-6 describe the log
interface. Any addition must stay greppable and must not log request or response
content (ADR-0010, §2 non-goal 3).

## Decision Drivers

- FR-10, FR-11 — the target's healthy / not healthy state that changes
- IF-6, §5.6 — the log interface and its line-per-event format
- ADR-0010 — the existing wake and error lines and the no-content rule
- §2 non-goal 3 — no request or response content is ever written

## Considered Options

1. Keep ADR-0010 as is: no health line.
2. One line per *belief change*, emitted at the single transition seam (chosen).
3. One line per *probe*, healthy or not.

## Decision Outcome

Chosen option: "One line per belief change". `Health.observe` already dedupes
transitions, so it now reports whether the belief changed, and the `Prober` —
the only production writer — logs `<RFC3339> health state=healthy` or
`<RFC3339> health state=unhealthy` once per change. Both the cadence loop and
the on-demand `ProbeNow` (FR-19's probe after a transport failure) go through
the same `record`, so a healthy→unhealthy flip caused by a forward failure is
logged too. The initial not-healthy state of a fresh process is not a change and
is not logged.

This extends ADR-0010 rather than replacing it: that ADR's decision about wake
and error lines and the no-content rule still stands.

### Consequences

- Good: an operator can see each reachability transition directly, greppable as
  `health state=`.
- Good: the line carries no request or response content, so §2 non-goal 3 holds.
- Good: the count of these lines is the number of transitions, not the number of
  probes, so a flapping or slow endpoint does not flood the log.
- Bad: the log is no longer only wake and error lines, so anything that counts
  total lines must count by form. FR-3, FR-9, and FR-12 count executions, not
  total lines, and their tests already match on `wake command=`.
- Bad: the state line is not tied to a single SRS requirement, so it is an
  operational convenience rather than a checked interface behaviour; its tests
  live in the logging suite.

## Pros and Cons of the Options

### Keep ADR-0010 as is (no health line)

- Good, because the log stays minimal and its total-line count stays meaningful.
- Bad, because an operator cannot tell when hypha became reachable except by
  reading probe behaviour or correlating a wake line with a client response.

### One line per belief change

- Good, because it is emitted exactly at the transition, so it is precise.
- Good, because `observe` is the single place the belief changes, so no caller
  can log a non-change or miss a change.
- Bad, because it adds a third line form to document and to match on.

### One line per probe

- Good, because it would show every observation, including a flapping endpoint.
- Bad, because it floods the log at the probe cadence, which is the opposite of
  ADR-0010's greppable, low-volume design.

## Confirmation

`TestADR0017LoggerHealthLine` covers the line form;
`TestADR0017ProberLogsStateChange` covers one line per change, none while the
belief is unchanged, and the reverse transition through `ProbeNow`;
`TestADR0017HealthTransitionLoggedEndToEnd` covers the whole path. The existing
FR-3/FR-9/FR-12 tests count wake lines by form, so the new line does not affect
them.

## Links

- Requirements: FR-10, FR-11, IF-6
- Related ADRs: ADR-0010 (the line set this extends), ADR-0008 (a probe is the
  only writer of the belief)
- Architecture document: §8.1 Logging
