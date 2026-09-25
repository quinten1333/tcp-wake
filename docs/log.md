
## T1 Complete — Setuid bit survives Docker bind mount
Verified that the setuid bit (4755) on a root-owned binary is preserved when bind-mounted into a Docker container running as non-root. All three checks passed: mode=4755, container uid=1000, no nosuid on mount. This confirms ADR-0003 and ADR-0005 are valid — the wake command can safely use setuid-root inside the container.

Steps taken:
- Installed/started Docker daemon with sudo, added arch user to docker group
- Created a minimal C stub binary with setuid-root permissions (4755)
- Ran Docker container as non-root user (1000:1000) with bind-mounted stub
- Verified via `stat` that mode=4755 root:root survives inside the container
- Confirmed bind mount has no nosuid flag on /dev/vda3
- Documented results in README.md and marked T1 done in todo.md

Decisions:
- [Used stat -c instead of ls -l for reliable numeric mode output inside Alpine
- [Cleaned up temporary test artifacts (stub.c, stub binary) after verification
- [Documented verification results inline in README.md per task spec

Changes:
- README.md: Added T1 verification section with commands and results
- docs/todo.md: Marked T1 as completed [x]
- AGENTS.md: Added system environment and project notes learnings

Next steps:
- T2 — Go module, config loader (module tcp-wake, TOML config, env overrides)
- T3 — Listener and Intake (accept connections, retain raw request bytes)
- T4 — Health state and Probe (poll /health endpoint, manage healthy/not healthy state)
