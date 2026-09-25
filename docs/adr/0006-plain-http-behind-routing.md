# ADR-0006: Plain HTTP on a configured address, TLS left to the existing routing

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

The system accepts a request from a client and returns hypha's response over the same connection (IF-1), and forwards held requests to hypha (IF-2). The spec says nothing about encryption, and client authentication is explicitly outside this system (non-goal 6). What the system listens on therefore decides whether TLS exists in one place or two.

## Decision Drivers

- IF-1 — accept a request over HTTP and return the response over the same connection
- IF-2 — forward a held request to hypha over HTTP
- §2 non-goal 6 — client authentication is the existing routing's job
- ADR-0002 — the system sits behind the existing routing

## Considered Options

1. Plain HTTP on a configured address, TLS left to the existing routing
2. TLS terminated in the system
3. HTTP/2 or HTTP/3

## Decision Outcome

Chosen option: "Plain HTTP on a configured address, TLS left to the existing routing", because the client-facing edge already exists and already owns certificates (ADR-0002); terminating TLS a second time would mean a second certificate lifecycle for one route.

### Consequences

- Good: one certificate lifecycle on hyperion instead of two, and the system needs no certificate material at rest.
- Good: the listener is the simplest thing that can hold a connection, which keeps the hold path (ADR-0004) free of handshake state.
- Bad: traffic between the existing routing and the system is unencrypted. On hyperion that is loopback or the host's own network stack, so it does not leave the machine — but it does mean the system must never be exposed on an address reachable from outside the host, or prompts and tokens would travel in the clear.
- Bad: if a future client reaches the system's port directly rather than through the routing, it gets no TLS and no authentication, and nothing in the system would refuse it.

## Pros and Cons of the Options

### Plain HTTP on a configured address, TLS left to the existing routing

- Good, because it keeps one edge, one certificate, and one place where client identity is decided.
- Bad, because it relies on a deployment property — that the port is not externally reachable — which is not enforced by the system itself.

### TLS terminated in the system

- Good, because the system would be safe to expose directly.
- Bad, because it duplicates certificate management for a route that already has it.
- Bad, because it puts handshake and session state into the process that holds long-lived connections, which is where the hold path's simplicity comes from.

### HTTP/2 or HTTP/3

- Good, because multiplexing would let many held requests share one client connection.
- Bad, because no requirement asks for it, and FR-13's byte-identical forwarding is defined against a single request stream.
- Bad, because HTTP/3 would add QUIC to a path whose whole job is to hold a TCP connection open.

## Confirmation

Inspection: the listener's configured address is not reachable from outside hyperion, and no certificate material is configured or stored. The system's own configuration contains no TLS keys.

## Links

- Requirements: IF-1, IF-2, §2 non-goal 6
- Related ADRs: ADR-0002 (the routing that terminates TLS), ADR-0011 (where the listen address is configured)
- Architecture document: §3 Context and Scope, §8 Cross-cutting Concepts (security)
