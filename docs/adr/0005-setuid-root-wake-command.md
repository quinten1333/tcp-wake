# ADR-0005: setuid-root wake command

```yaml
---
status: superseded by ADR-0016
date: 2026-09-23
decision-makers: [the user]
consulted: []
informed: []
---
```

## Context and Problem Statement

The wake command needs elevated privileges on hyperion (C-4), and NFR-7 requires that it actually receive them, while NFR-8 requires every other part of the system to run without them. The system is therefore unprivileged and must still be able to invoke a privileged command, and the boundary between the two must be inspectable.

## Decision Drivers

- NFR-7 — the wake command shall execute with the privileges it requires
- NFR-8 — every part of the system other than the wake command runs without elevated privileges
- C-4 — the wake command requires elevated privileges on hyperion
- §5.4 — one invocation per triggering request, with the exit status observable

## Considered Options

1. setuid-root wake command
2. A sudoers rule for that one command
3. A root helper daemon

## Decision Outcome

Chosen option: "setuid-root wake command", because the privilege becomes a property of the command file itself — mode 4755, owned by root — so the boundary is readable with `ls -l` and needs no daemon, no sudoers edit, and no inter-process protocol.

### Consequences

- Good: NFR-7 and NFR-8 are both checkable by inspection — the command's file mode and owner on one side, the proxy process's uid on the other.
- Good: the invocation is an ordinary process execution, so §5.4's "exit status observable" needs nothing beyond reading the child's exit status.
- Bad: setuid on a dynamically linked binary is fragile, and inside a container (ADR-0003) the runtime can strip the bit, which would break the wake path while leaving the file mode looking correct on the host.
- Bad: the command runs with full root privilege rather than the narrow privilege it needs, so a fault in the command is a root-level fault.

## Pros and Cons of the Options

### setuid-root wake command

- Good, because the boundary is one file's mode, which is the cheapest thing to audit.
- Good, because there is no long-running privileged process and no configuration surface to keep in step.
- Bad, because it grants full root rather than the minimum privilege required.
- Bad, because a container runtime can silently remove the bit.

### A sudoers rule for that one command

- Good, because the rule can name the exact command path and arguments, granting less than full root.
- Good, because sudo's own logging records each invocation, which is a second artefact for FR-3's counting.
- Bad, because it widens the boundary to sudo and its configuration, so the audit surface is the rule file plus sudo's behaviour rather than one file mode.
- Bad, because a mis-scoped rule grants more than intended, and the mistake is invisible to `ls -l`.

### A root helper daemon

- Good, because the proxy would never invoke anything privileged directly, keeping the trust boundary at one socket.
- Bad, because it adds a long-running privileged process, which is the opposite of NFR-8's intent.
- Bad, because it introduces a protocol, a socket, and a supervision problem for a single command invocation.

## Confirmation

Inspection: the wake command's file mode and owner (4755, root:root) read from inside the running container, and the proxy process's uid. NFR-7's check is that the command still carries its elevated bit after deployment.

## Links

- Requirements: NFR-7, NFR-8, C-4, §5.4
- Related ADRs: ADR-0003 (the container that must preserve the bit), ADR-0011 (where the command path is configured)
- Architecture document: §8 Cross-cutting Concepts (privilege), §11 Risks
