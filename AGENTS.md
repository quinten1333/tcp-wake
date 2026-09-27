# Agent memory
This file is your memory. Update this file as the project progresses. You can create new sections as you see fit.

# Project architecture
tcp-wake is a wake-on-demand reverse proxy in Go deployed as a container on
hyperion in front of hypha (normally powered off). It holds hypha-bound requests
with no deadline, execs a setuid-root wake command, polls hypha's `/health`, and
forwards held requests verbatim once healthy. Source of truth: `docs/architecture.md`
(arc42, 15 ADRs accepted) plus `specs/SRS.md` and `docs/implementation-plan.md`.
Build order: T1 (blocking prerequisite) then T2–T16. See `docs/implementation-plan.md`.

# Learnings
- **T1 / setuid in containers.** The setuid effect depends on the mount options,
  not just the file mode. `/tmp` on this host is a `nosuid` tmpfs, and a Docker
  bind mount inherits the source filesystem's options, so a stub built under
  `/tmp` shows mode `4755` but executes with no privilege. Build the stub in the
  repository checkout (btrfs root subvolume, no `nosuid`). The definitive check is
  to **execute** the binary and read `geteuid()`, not to `stat` it. Verified PASS
  inside `debian:stable-slim` as uid 1000 (`effective_uid=0`).
- Use a glibc-based image (`debian:stable-slim`) to run glibc-compiled stubs;
  Alpine's musl gives a misleading `not found` on a glibc dynamic binary.
- **Environment.** Arch Linux VM; `arch` user is in the `docker` group; Docker
  29.8.1 with daemon active. `sudo` is available for root-owned artifacts.

# Workflow and process
- **Commit format:** the user wants `[<n>] <Summarized task title> <Summary of Change>`,
  and a push after each milestone.
- **Plan tracking:** tick each task's checkbox in `docs/implementation-plan.md` as it
  lands, and append a dated entry to `docs/log.md`.
- **Traceability check** (the only check that exists so far):
  `python3 scripts/check_traceability.py docs/specs/SRS.md docs/architecture.md`
  — note the spec is at `docs/specs/SRS.md`, not `specs/SRS.md`. It must exit 0.
- **Repo state:** there is no Go module yet (T2 creates it), so there are no Go
  linters or tests to run. The `scripts/` dir holds the traceability checker.
- **Don't commit build artifacts.** T1's `stub`/`stub.c` are gitignored.
