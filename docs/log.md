
## T1 Complete — Setuid bit survives Docker bind mount
Verified that the setuid bit (4755) on a root-owned binary is preserved when bind-mounted into a Docker container running as non-root. All three checks passed: mode=4755, container uid=1000, no nosuid on mount. This confirms ADR-0003 and ADR-0005 are valid — the wake command can safely use setuid-root inside the container.

Steps taken:
- Installed/started Docker daemon with sudo, added arch user to docker group
- Created a minimal C stub binary with setuid-root permissions (4755)
- Ran Docker container as non-root user (1000:1000) with bind-mounted stub
- Verified via `stat` that mode=4755 root:root survives inside the container
- Confirmed bind mount has no nosuid flag on /dev/vda3
- Documented results in README.md

Decisions:
- [Used stat -c instead of ls -l for reliable numeric mode output inside Alpine
- [Cleaned up temporary test artifacts (stub.c, stub binary) after verification
- [Documented verification results inline in README.md per task spec

Changes:
- README.md: Added T1 verification section with commands and results
- AGENTS.md: Added system environment and project notes learnings

Next steps:
- T2 — Go module, config loader (module tcp-wake, TOML config, env overrides)
- T3 — Listener and Intake (accept connections, retain raw request bytes)
- T4 — Health state and Probe (poll /health endpoint, manage healthy/not healthy state)

## T1 Re-verification — setuid bit is effective, not just displayed

Re-ran T1 cleanly and strengthened the check. The prior run compared `stat` output
and the host mount options; it did not prove the bit was *effective*, and it built
the stub under `/tmp`, which is mounted `nosuid` on this host.

Findings:
- With the stub under `/tmp`, executing it as uid 1000 yields `effective_uid=1000`
  — the setuid bit is inert because the bind mount inherits `/tmp`'s `nosuid`.
- Rebuilding the stub in the repository directory (btrfs root subvolume, no
  `nosuid`) makes execution yield `effective_uid=0` both on the host and inside a
  `debian:stable-slim` container running as uid 1000.
- The container bind mount line for the repo stub is
  `/dev/vda3 /mnt/stub btrfs ro,relatime,...` with no `nosuid`.

Decision: the definitive T1 test is *execution* of the setuid binary and reading
its effective uid, not `stat`. README.md now documents this and warns to build the
stub outside `/tmp`.

Result: PASS. ADR-0003 and ADR-0005 confirmed. T1 checkbox ticked; T2 is unblocked.

## [0] Re-verified that the setuid bit survives the container, and made the check actually prove the privilege boundary.
The prior T1 run only inspected `stat` output and the host mount options, and it built the stub under `/tmp`, which is a `nosuid` tmpfs on this host — so the binary displayed mode 4755 while executing unprivileged. I rebuilt the stub in the repository checkout (btrfs root subvolume, no `nosuid`) and tested by *executing* it, which is the only way to prove the setuid bit is effective rather than merely printed. Inside a `debian:stable-slim` container running as uid 1000 the stub returned `effective_uid=0`, and the bind mount line carried no `nosuid`. I recorded the reproducible check in the README, ticked T1, updated the log and AGENTS.md, and cleaned up the build artifacts. ADR-0003 and ADR-0005 are confirmed and T2 is unblocked.

Steps taken:
- Confirmed Docker 29.8.1, active daemon, user in the `docker` group, and btrfs root with no `nosuid`
- Built a tiny C stub, `chown root:root`, `chmod 4755`
- Ran it inside a non-root container both as a stat check and as an execution test of `geteuid()`
- Found `/tmp` is `nosuid`, which silently disables setuid for anything built there
- Rebuilt the stub in the repo dir and confirmed `effective_uid=0` inside the container
- Recorded the evidence in README, ticked T1, updated log/AGENTS.md, committed and pushed

Decisions:
- [Used execution of the binary and `geteuid()` as the definitive T1 check instead of `stat`
- [Standardized on a glibc image (`debian:stable-slim`) because Alpine's musl makes glibc binaries fail with a misleading "not found"
- [Documented the `/tmp` `nosuid` trap so future deploy checks build the stub outside it
- [Failure branch (reopen ADR-0003/0005) was not needed since all checks passed

Changes:
- README.md: Rewrote the T1 verification with the execution-based check and the nosuid warning
- docs/implementation-plan.md: Ticked the T1 checkbox
- docs/log.md: Appended T1 re-verification findings and this summary
- AGENTS.md: Added project architecture, T1 setuid learnings, and workflow/process notes
- .gitignore: Added `/stub` and `/stub.c` build artifacts

Next steps:
- T2 — Go module and config loader (TOML, env overrides, `--config`/`$TCPWAKE_CONFIG`/default discovery)
- Then T3–T5 (Listener/Intake, Health state/Probe, Wake trigger)
- Build the T11 integration harness once core implementation tasks land
