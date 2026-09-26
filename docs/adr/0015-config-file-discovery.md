# ADR-0015: Configuration file discovery and the `--config` flag

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

IF-5 and §5.5 require the key set to come from a configuration file and require that a missing or unreadable file prevents start, but they do not say which file. ADR-0011 settled file-versus-environment precedence and ADR-0013 settled the format; neither says how the file is located. The implementation already carries a `--config` flag and a four-step search (documented only in `config.example.toml`), so the behaviour exists without a recorded decision — an agent reading the architecture could invent a different search, or drop the flag, without contradicting any document.

## Decision Drivers

- IF-5 — the key set is read from a configuration file
- §5.5 — a missing or unreadable configuration file prevents start, reported at start
- ADR-0011 — a file is the base source, environment overrides win
- ADR-0003 — deployed as a container, where the config is mounted rather than edited in place
- T14 (implementation plan) — the image mounts the config at `/etc/tcp-wake/config.toml`

## Considered Options

1. `--config PATH`, else `$TCPWAKE_CONFIG`, else the fixed default `/etc/tcp-wake/config.toml` — no current-directory fallback
2. The current four-step search: `--config`, `$TCPWAKE_CONFIG`, `/etc/tcp-wake/config.toml`, then `./config.toml`
3. `--config PATH` only — the path is always explicit

## Decision Outcome

Chosen option: "`--config PATH`, else `$TCPWAKE_CONFIG`, else the fixed default `/etc/tcp-wake/config.toml` — no current-directory fallback", because a fixed default with two explicit override points is deterministic, and a current-directory default makes the effective config depend on where the process happens to be started.

### Consequences

- Good: the effective config is a function of the flag, the environment, and one fixed path, so "which file did it read?" always has an answer.
- Good: the mount point from the deployment plan (T14) is the default, so the container needs neither a flag nor an environment value to start correctly.
- Bad: a developer running the binary from a checkout must pass `--config` or set `$TCPWAKE_CONFIG`; the current-directory convenience is lost.
- Bad: the search order is still a second place — the code plus the architecture — that must agree, though it is now recorded here.

## Pros and Cons of the Options

### `--config`, `$TCPWAKE_CONFIG`, fixed default — no CWD fallback

- Good, because it is deterministic: a process started anywhere reads the same file unless told otherwise.
- Good, because the container mount point is the default, so the common deployment needs no extra configuration.
- Bad, because local development loses the "drop a config next to the binary" convenience.

### Current four-step search with `./config.toml`

- Good, because a checkout can carry its own config and run with no arguments.
- Bad, because the effective config depends on the working directory, so two starts of the same binary can behave differently.
- Bad, because the container's working directory — not the mount point — would decide which file wins if a stray `config.toml` were present.

### `--config` only

- Good, because there is exactly one mechanism and no precedence question.
- Bad, because the container would have to pass the flag or a command for the only file it ever uses.
- Bad, because `$TCPWAKE_CONFIG` is what a compose file can set without a command, which is the reason ADR-0011 added the environment at all.

## Confirmation

Test: with no flag and no environment, the fixed default path is read; `$TCPWAKE_CONFIG` overrides the default; `--config` overrides both; a resolved path that is missing or unreadable prevents start with a message naming it. Inspection: the deployed image's mount point equals the default.

## Links

- Requirements: IF-5, §5.5
- Related ADRs: ADR-0011 (file plus environment overrides), ADR-0013 (TOML format), ADR-0003 (container deployment)
- Architecture document: §5.5 Configuration interface, §8.2 Configuration
