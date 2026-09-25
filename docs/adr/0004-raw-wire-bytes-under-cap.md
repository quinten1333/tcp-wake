# ADR-0004: Retain held requests as raw wire bytes under a cap

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

A request received while hypha is not healthy must be kept until hypha is healthy (FR-2), then sent to hypha unchanged (FR-13), with its response returned unchanged (FR-14) and streamed chunk by chunk if it is a stream (FR-15). A body above the configured cap must be refused with an error naming the cap (FR-20), and no deadline may be applied while the request is held (NFR-4). How the request is retained decides whether "unchanged" is a guarantee or a hope.

## Decision Drivers

- FR-13 — transmit method, path, headers, and body unchanged
- FR-14 — transmit hypha's status, headers, and body unchanged
- FR-15 — relay each streamed chunk as it arrives
- FR-20 — a body above the cap gets an error naming the cap
- NFR-4 — no read or write deadline on a held connection
- NFR-5 — hold at least 8 concurrent requests

## Considered Options

1. Retain raw wire bytes in memory, under the cap
2. Parse the request and re-issue it with an HTTP client
3. Retain raw bytes, spooling over-cap bodies to disk

## Decision Outcome

Chosen option: "Retain raw wire bytes in memory, under the cap", because FR-13's byte-identical forwarding is guaranteed by never parsing the request: the bytes held are the bytes the client sent, so there is nothing to re-serialise incorrectly.

### Consequences

- Good: FR-13 and FR-14 hold by construction. Method, path, header order, header casing, and framing are preserved because they are never interpreted, only stored and re-sent.
- Good: the cap in FR-20 is measured on the exact bytes retained, so the check and the behaviour are the same number.
- Bad: worst-case memory is the cap multiplied by the number of held requests, so the cap and the held-request capacity together set a hard ceiling on the system's footprint.
- Bad: the framing logic must detect when a request is complete without interpreting it, so a client that stalls mid-body leaves a request that can never be forwarded — the case the spec puts out of scope.

## Pros and Cons of the Options

### Retain raw wire bytes in memory, under the cap

- Good, because it makes FR-13 a property of the storage rather than of a serialiser.
- Good, because streaming the response (FR-15) is then a copy, with no parsing of hypha's response either.
- Bad, because it needs framing detection — knowing when the body has ended — without full parsing.

### Parse the request and re-issue it with an HTTP client

- Good, because an HTTP client library handles framing, redirects, and connection pooling for you.
- Bad, because re-serialising a parsed request changes header order and casing, and may change framing, which fails FR-13's byte comparison.
- Bad, because it would also parse hypha's response, putting a second interpretation between hypha and the client and endangering FR-14.

### Retain raw bytes, spooling over-cap bodies to disk

- Good, because it would allow bodies larger than memory to be held.
- Bad, because FR-20 requires an error above the cap, not a different storage tier, so spooling would contradict the requirement rather than satisfy it.
- Bad, because it adds a disk-write path and a cleanup obligation for a case the spec explicitly caps away.

## Confirmation

Test: the request as recorded in hypha's access log is byte-compared against the client's original for held requests (FR-13). Test: a body above the configured cap receives an error naming the cap (FR-20).

## Links

- Requirements: FR-13, FR-14, FR-15, FR-20, NFR-4, NFR-5
- Related ADRs: ADR-0001 (the runtime that carries this), ADR-0012 (whether the held set is capped)
- Architecture document: §5 Building Block View, §6 Runtime View
