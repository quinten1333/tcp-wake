# ADR-0013: TOML as the configuration format

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

IF-5 requires the wait bound, the wake command, hypha's address, the health endpoint path, the probe cadence, the probe timeout, and the held body cap to come from a configuration file, with environment-variable overrides (ADR-0011). ADR-0011 settled *where* values come from and the precedence between the two sources; it did not settle the file's syntax. The choice matters here more than it usually would, because several keys carry constraints that a reader cannot infer from the value — the setuid requirement on the wake command, and why the wait bound is measured from arrival.

## Decision Drivers

- IF-5 — the key set is read from a configuration file
- ADR-0011 — a file plus environment overrides, environment wins
- ADR-0003 — deployed as a container, so the file is mounted rather than edited in place
- ADR-0005 — the wake command path is configuration, and it carries a privilege requirement
- ADR-0007 — the probe cadence and timeout are configuration

## Considered Options

1. TOML, with one small third-party dependency
2. JSON, using the Go standard library only
3. YAML, with a third-party dependency

## Decision Outcome

Chosen option: "TOML, with one small third-party dependency", because the configuration carries constraints that need explaining next to the values, and TOML is the only one of the three that supports comments. It is also the format `auto-shutdown` already uses on this fleet, so the two configuration files an operator meets on hyperion look alike.

### Consequences

- Good: every non-obvious key can carry its reason inline — the setuid requirement beside `wake_command`, the "never restarted" rule beside `wait_bound` — which is exactly what stops an operator changing a value that looks arbitrary.
- Good: consistent with `auto-shutdown`'s configuration on the same host, so there is one configuration idiom on hyperion rather than two.
- Good: TOML's types are unambiguous and its parser is small and stable, so the dependency is low-risk.
- Bad: Go's standard library has no TOML parser, so the system gains its first third-party dependency. This is a real cost for a service whose only job is to be running when nothing else is.
- Bad: TOML has no native duration or size type, so those values are strings that must be parsed and validated at start ("120s", "64MiB"). A typo in one is a start-time failure rather than a type error.

## Pros and Cons of the Options

### TOML, with one small third-party dependency

- Good, because comments are the whole reason: this file has to explain itself.
- Good, because it matches the existing configuration style on hyperion.
- Bad, because it introduces a dependency to a system that would otherwise have none.

### JSON, using the Go standard library only

- Good, because it needs no dependency at all — `encoding/json` is in the standard library.
- Bad, because JSON has no comments, so every constraint that explains a value would have to live in a separate document that can drift from the file.
- Bad, because JSON is noisy for a flat key set, and its lack of trailing commas makes editing by hand more error-prone.

### YAML, with a third-party dependency

- Good, because it supports comments as TOML does.
- Bad, because it buys nothing TOML does not, and YAML's whitespace sensitivity and implicit typing (where `no` can become a boolean) are exactly the wrong properties for a file whose values must not be misread.
- Bad, because it is the largest and least predictable of the three parsers for a flat key set.

## Confirmation

Test: each key is changed in the file and the changed behaviour is observed. Test: a key set in both the file and the environment takes the environment's value. Inspection: a malformed value — an unparseable duration or size — prevents start with a message naming the key.

## Links

- Requirements: IF-5, §5.5
- Related ADRs: ADR-0011 (file plus environment overrides), ADR-0005 (the wake command key), ADR-0007 (the probe keys), ADR-0004 (the cap key)
- Architecture document: §8 Cross-cutting Concepts (configuration)
