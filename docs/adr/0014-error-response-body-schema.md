# ADR-0014: Error response body schema

```yaml
---
status: accepted
date: 2026-09-26
decision-makers: [the user]
consulted: []
informed: []
---
```

## Context and Problem Statement

ADR-0009 chose a JSON body naming the failed component for each of the four error paths, and SRS §5.1 requires the body to name the component or limit responsible. The field names and the exact shape are not settled anywhere, so an implementer must invent the schema and a client must guess it — the one place the architecture still leaves an interface without a contract. This is an internal system, where the clients are agents and editors that are expected to branch on the reason rather than read prose.

## Decision Drivers

- FR-8 — the wait-bound error names the wait bound
- FR-17 — the wake-command error names the wake command
- FR-18 — the transport error names hypha as unreachable
- FR-20 — the body-cap error names the held body cap
- §5.1 — each error body names the component or limit responsible
- NFR-6 — exactly one response per accepted request
- ADR-0009 — JSON bodies, machine-readable, so agents can branch
- G2 — while hypha is healthy the system is invisible; error output should not be the one place it surprises a client

## Considered Options

1. A nested `error` object carrying `message`, `component`, and an optional `limit`
2. Flat top-level fields: `error` (message), `component`, `limit`
3. RFC 7807 `application/problem+json` (`type`, `title`, `status`, `detail`)

## Decision Outcome

Chosen option: "A nested `error` object carrying `message`, `component`, and an optional `limit`", because hypha's own health endpoint already answers with `{"error":{"message":"Loading model"}}`, so a client that already parses hypha errors gets the same envelope from the system — and matching the origin is what keeps the proxy invisible (G2).

### Consequences

- Good: the `component` value is an enumerable token (`wait_bound`, `wake_command`, `target`, `held_body_cap`), so an agent can branch without parsing prose, which is what ADR-0009 promised.
- Good: the `limit` field carries the bound or the cap as a value, so a client can adapt rather than only report.
- Good: the envelope matches hypha's, so one parser handles both the origin's errors and the proxy's.
- Bad: the system now owns a versioned output contract; a field-name change is a breaking change for clients. This is a risk against which the schema must be kept stable.
- Bad: a health-probe response and an error body are two JSON shapes to keep tested.

## Pros and Cons of the Options

### Nested `error` object with `message`, `component`, `limit`

- Good, because it mirrors hypha's `{"error":{"message":…}}` envelope, so the system stays invisible to the client even when it errors.
- Good, because `component` can be an enumerated token and `limit` a typed value, which makes the body branchable.
- Bad, because it is a bespoke schema the system must document and keep stable.

### Flat top-level fields

- Good, because it is the least nesting to parse and the smallest body.
- Bad, because it diverges from the origin's envelope, so a client needs a second parser for the proxy's errors.
- Bad, because `error` as a string here and as an object at hypha is exactly the inconsistency G2 asks the system not to introduce.

### RFC 7807 `application/problem+json`

- Good, because it is a standard shape with an existing client ecosystem and no bespoke contract to maintain.
- Bad, because its `type` field is a URI vocabulary the project would have to invent and host, which is more machinery than four static conditions justify.
- Bad, because no client in this system speaks it, while hypha's own error envelope is already `{"error":{"message":…}}`.

## Confirmation

Test: each of the four conditions is triggered and the response's status and exact JSON shape are asserted — 504 with `component=wait_bound` and the configured bound in `limit`, 500 with `component=wake_command`, 502 with `component=target`, 413 with `component=held_body_cap` and the configured cap in `limit`. Inspection: hypha's health-error envelope is compared against the chosen shape.

## Links

- Requirements: FR-8, FR-17, FR-18, FR-20, §5.1, NFR-6
- Related ADRs: ADR-0009 (JSON bodies), ADR-0004 (the cap reported by 413), ADR-0008 (the transport failure reported by 502)
- Architecture document: §5.1 Error responses, §8.4 Error handling
