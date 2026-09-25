# ADR-0011: Config file with environment-variable overrides

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

IF-5 requires the wait bound, the wake command, hypha's address, the health endpoint path, the probe cadence, the probe timeout, and the held body cap to come from a configuration file. The system is deployed as a container (ADR-0003), and the user requires that environment variables also be settable, because that is what a compose file can supply.

## Decision Drivers

- IF-5 — read the configuration keys from a configuration file
- ADR-0003 — the system is deployed as a container
- ADR-0005 — the wake command path is configuration
- §5.5 — a missing or unreadable configuration file prevents start

## Considered Options

1. Config file only
2. Config file with environment-variable overrides
3. Environment variables only

## Decision Outcome

Chosen option: "Config file with environment-variable overrides", because the file is the inspectable artefact of record while the environment is what a compose file can set without mounting anything. When a key is present in both, the environment variable wins, so a stale mounted file cannot silently override what the deployment states.

### Consequences

- Good: the file documents the full key set in one place, and the environment can adjust any of it at deployment time without editing the file.
- Good: the deployment's own declaration wins over a file that may be left over from an earlier deployment, which is the safer direction for the override to point.
- Bad: a key can be set in two places, so a value that appears not to take effect has two candidate causes.
- Bad: the effective configuration is the file plus the environment, which means an operator must read both to know what the system is running with. This is the cost of the override the user asked for.

## Pros and Cons of the Options

### Config file only

- Good, because there is exactly one place to look.
- Bad, because a container deployment would have to mount a file to change any value, which is what the user rejected.

### Config file with environment-variable overrides

- Good, because it gives the compose file a way to set values without mounting anything.
- Good, because the file remains a complete, readable default for a non-container deployment.
- Bad, because the precedence rule has to be documented and remembered.

### Environment variables only

- Good, because there is one mechanism and no precedence question.
- Bad, because IF-5 requires a configuration file, so this option contradicts the spec.
- Bad, because a container's environment is not an inspectable artefact in the way a file is.

## Confirmation

Test: each key is changed in the file and the changed behaviour is observed. Test: a key set in both the file and the environment takes the environment's value. Inspection: a missing configuration file prevents start and the failure is reported.

## Links

- Requirements: IF-5, §5.5
- Related ADRs: ADR-0003 (the deployment that needs the override), ADR-0007 (the cadence keys), ADR-0004 (the cap key)
- Architecture document: §8 Cross-cutting Concepts (configuration)
