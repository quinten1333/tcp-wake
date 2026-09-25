# Decision Map — tcp-wake

> One row per design decision. Every NFR (SRS §4), every interface (SRS §5), and every constraint (SRS §2) yields at least one row. The architecture is not baseline while any row's ADR status is not `accepted`.
> Conforms to: `specs/SRS.md` baseline v0.1.

| # | Source (SRS ID/section) | Decision area | Question to the user | Recommended option | ADR | Status |
|---|---|---|---|---|---|---|
| 1 | FR-2, FR-5, FR-15, NFR-3, NFR-4 | Strategy — implementation technology | What hosts the hold, the parallel release, and the streamed relay? A) standalone Go service, B) nginx plus a custom module or Lua, C) hand-rolled C proxy | A | ADR-0001 | open |
| 2 | FR-9, §2 non-goal 6, FR-1 | Strategy — integration | Where does the system sit relative to hyperion's existing routing? A) dedicated listen address behind the existing routing, B) replace the existing proxy, C) a module inside it | A | ADR-0002 | open |
| 3 | NFR-7, NFR-8, C-1, C-4 | Deployment — form | How is the system deployed on hyperion? A) systemd unit, no container, B) container with host networking, C) container with bridge networking | A | ADR-0003 | open |
| 4 | FR-13, FR-14, FR-15, FR-20, NFR-4, NFR-5 | Decomposition — hold mechanism | How is a held request retained and re-sent? A) raw wire bytes in memory under the cap, B) parsed and re-issued by an HTTP client, C) bodies over the cap spooled to disk | A | ADR-0004 | open |
| 5 | NFR-7, NFR-8, C-4, §5.4 | Interface — wake-command privilege | How does the unprivileged proxy run a command that needs elevated privileges? A) setuid-root command, B) sudoers rule for one command, C) a root helper daemon | A | ADR-0005 | open |
| 6 | IF-1, IF-2, §2 non-goal 6 | Interface — listen surface | What does the system listen on? A) plain HTTP on a configured address, TLS left to the existing routing, B) TLS terminated in the system, C) HTTP/2 or HTTP/3 | A | ADR-0006 | open |
| 7 | FR-10, FR-11, IF-3, NFR-1 | Interface — probe parameters | What probe cadence and per-probe timeout apply while a request is pending? A) 1 s interval and 1 s timeout, B) 2 s and 1 s, C) 500 ms and 500 ms | A | ADR-0007 | open |
| 8 | FR-1, FR-18, FR-19, NFR-2 | Behaviour — health belief | Does the system probe before each forward while it believes hypha healthy? A) trust the cached state and let FR-18/FR-19 catch a failure, B) probe before every forward, C) probe on a timer | A | ADR-0008 | open |
| 9 | FR-8, FR-17, FR-18, FR-20, §5.1 | Interface — error responses | What shape do the four error responses take? A) JSON body naming the component and the bound, B) plain-text body, C) status code only | A | ADR-0009 | open |
| 10 | IF-6, FR-3, FR-9, FR-12 | Cross-cutting — logging | What is logged, and in what format? A) one line per wake execution plus one per error, B) structured JSON to journald, C) full request logging | A | ADR-0010 | open |
| 11 | IF-5, §5.5 | Cross-cutting — configuration | How is the system configured? A) one config file plus CLI overrides, B) environment variables, C) a systemd drop-in per key | A | ADR-0011 | open |
| 12 | NFR-5, FR-2, §2 non-goal 5 | Behaviour — held-request capacity | Is the held set capped? A) unbounded, B) capped with an error above the cap, C) capped with a queue that blocks new accepts | A | ADR-0012 | open |

## Coverage check

- **NFRs**: NFR-1 → rows 7, 12; NFR-2 → rows 6, 8; NFR-3 → rows 1, 6; NFR-4 → rows 1, 4; NFR-5 → rows 4, 12; NFR-6 → rows 4, 9, 12; NFR-7 → rows 3, 5; NFR-8 → rows 3, 5.
- **Interfaces**: IF-1 → rows 2, 6; IF-2 → rows 1, 4; IF-3 → row 7; IF-4 → row 5; IF-5 → row 11; IF-6 → row 10.
- **Constraints**: C-1 → row 3; C-2 → rows 7, 12; C-3 → rows 5, 12; C-4 → rows 3, 5; C-5 → row 7.
- **Settled by the requirements, so not decisions**: parallel release ordering (FR-5 forces it; one option), probing after a transport failure (FR-19 forces it; one option), and the absence of a connection timeout on held requests (NFR-4 forbids one).
