# ADR-0003: Container with host networking

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

NFR-8 requires every part of the system except the wake command to run without elevated privileges, while C-4 requires the wake command to have them and NFR-7 requires it to actually receive them. The deployment form decides whether that privilege boundary survives, and whether the system's listen address and hypha's address are reachable as the existing routing expects.

## Decision Drivers

- NFR-7 — the wake command shall execute with the privileges it requires
- NFR-8 — everything except the wake command runs without elevated privileges
- C-1 — hyperion is the always-on host and the only one
- C-4 — the wake command requires elevated privileges on hyperion

## Considered Options

1. systemd unit, no container
2. Container with host networking
3. Container with bridge networking

## Decision Outcome

Chosen option: "Container with host networking". The recommendation put to the user was option 1, and the user rejected it: everything on hyperion is deployed as a container, so a systemd unit would be the single exception and the odd one out operationally. Host networking keeps the listen address and hypha's address on the host's own network stack, which is what the routing entry (ADR-0002) and the wake target assume.

### Consequences

- Good: the system is deployed the same way as everything else on hyperion, so there is one deployment mechanism to operate and document rather than two.
- Good: with host networking there is no NAT between the system and hypha, so the probe and the forward reach the same addresses the routing layer uses, and no port mapping has to be kept in step.
- Bad: a container runtime becomes a host dependency of a system whose only job is to run when nothing else is.
- Bad: a setuid binary inside a container can have its setuid bit stripped by the runtime or the storage driver, which would silently break the wake path. This is the risk this decision creates, and it must be tested at deployment rather than assumed.

## Pros and Cons of the Options

### systemd unit, no container

- Good, because it removes the container runtime from the dependency list entirely.
- Good, because setuid on the wake command behaves exactly as the file mode says, with no runtime in between to strip it.
- Bad, because it makes this system the only thing on hyperion not deployed as a container, which the user judged the larger cost.

### Container with host networking

- Good, because it matches the existing deployment mechanism on hyperion.
- Good, because host networking removes a layer of address translation between the system and both the client-side routing and hypha.
- Bad, because the setuid boundary now depends on the container runtime's behaviour, which is a new failure mode for NFR-7.

### Container with bridge networking

- Good, because it isolates the system's network namespace from the host's.
- Bad, because it would put NAT between the system and hypha, so the wake target address and the probe address would have to be expressed twice and kept in step.
- Bad, because it buys isolation no requirement asks for, at the cost of a mapping that can drift.

## Confirmation

Inspection: the container's specification (host networking on, no elevated capabilities for the proxy process) together with the wake command's file mode and owner read from inside the running container. NFR-7's check is that the command still carries its elevated bit after the container has started.

## Links

- Requirements: NFR-7, NFR-8, C-1, C-4
- Related ADRs: ADR-0005 (the privilege boundary this must preserve), ADR-0002 (the routing that reaches this container)
- Architecture document: §7 Deployment View, §11 Risks
