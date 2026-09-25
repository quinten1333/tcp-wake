# ADR-0001: Go as the proxy implementation

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

The spec forces three behaviours that must be hosted by a concrete runtime: holding a request open with no deadline at all (FR-2, NFR-4), releasing every held request without waiting for any other (FR-5), and relaying a streamed response chunk by chunk as the chunks arrive (FR-15, NFR-3). The choice of runtime decides whether those are structural properties or fights against the framework.

## Decision Drivers

- FR-2 — hold a request open without sending a response
- FR-5 — begin forwarding each held request without waiting for any other
- FR-15 — relay each streamed chunk as it arrives
- NFR-3 — no more than 50 ms added latency per relayed chunk
- NFR-4 — no read or write deadline on a held connection
- NFR-5 — hold at least 8 concurrent requests

## Considered Options

1. Standalone Go service
2. nginx with a custom module or Lua
3. Hand-rolled C proxy

## Decision Outcome

Chosen option: "Standalone Go service", because Go's goroutine-per-connection model makes holding many connections with no deadline and then releasing them in parallel the default behaviour rather than something to work around, and `io.Copy` relays a response body chunk by chunk without buffering it whole.

### Consequences

- Good: FR-5 and NFR-4 are satisfied by the runtime's normal operation. A held request is a blocked goroutine, and releasing it is unblocking that goroutine; nothing has to be scheduled or serialised.
- Good: FR-15 and NFR-3 are satisfied by streaming a copy rather than assembling a response, so no chunk is delayed by a later one.
- Bad: adds a Go toolchain to hyperion's build and a second HTTP implementation to keep patched, alongside the existing routing layer.

## Pros and Cons of the Options

### Standalone Go service

- Good, because per-connection state with no deadline is idiomatic, so NFR-4 needs no special configuration.
- Good, because parallel release (FR-5) is the natural consequence of one goroutine per held request.
- Good, because it builds to one static binary, which keeps the container image small (ADR-0003).
- Bad, because it introduces a language and toolchain not otherwise required on hyperion.

### nginx with a custom module or Lua

- Good, because nginx is already present on hyperion for the existing routing (ADR-0002).
- Bad, because nginx's proxy model is built around timeouts and upstream connection reuse; holding a connection open indefinitely (NFR-4) is fighting the configuration rather than using it.
- Bad, because releasing held requests in parallel (FR-5) would have to be expressed in Lua or a C module, which is where the complexity and the bugs live.
- Bad, because the hold state would live inside the process that also serves every other vhost, so an unrelated reload would risk it.

### Hand-rolled C proxy

- Good, because it has the smallest runtime footprint of the three.
- Bad, because holding many connections and relaying streams correctly is exactly the work the Go runtime already does, and writing it by hand adds memory-safety risk for no requirement gain.

## Confirmation

Inspection of the built artefact: one binary, with held connections represented as blocked goroutines and no deadline set on them. NFR-4's check is the direct inspection of that absence.

## Links

- Requirements: FR-2, FR-5, FR-15, NFR-3, NFR-4, NFR-5
- Related ADRs: ADR-0004 (the hold mechanism this runtime carries), ADR-0003 (how it is deployed)
- Architecture document: §4 Solution Strategy, §5 Building Block View
