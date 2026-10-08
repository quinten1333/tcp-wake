# ADR-0016: Fixed etherwake in the image with a NET_RAW file capability

```yaml
---
status: accepted
date: 2026-10-08
decision-makers: [the user]
consulted: []
informed: []
---
```

## Context and Problem Statement

ADR-0005 makes the elevated privilege a property of a setuid-root file
bind-mounted from the host. That gives an auditable boundary (`ls -l` shows
`4755 root:root`), but it creates R-1: a container runtime or a `nosuid` bind
mount can strip the bit while the host mode still reads `4755`, silently
breaking the wake path. It also requires the operator to author and mount a wake
command on every host. The user asked for the wake tool to live in the
container and for the wake command to always be `etherwake`, so this decision
replaces ADR-0005's mechanism and narrows configuration to the target's MAC
address (plus an optional interface). NFR-7 requires the wake command to
actually receive the privilege it needs; NFR-8 requires everything else to stay
unprivileged; C-4 says the wake needs elevated privilege. `etherwake` needs a
raw socket (`CAP_NET_RAW`), so the question is how to grant exactly that,
without `sudo` and without a configurable command.

## Decision Drivers

- C-4 — the wake command requires elevated privileges on hyperion
- NFR-7 — the wake command shall execute with the privileges it requires
- NFR-8 — every part of the system other than the wake command runs without them
- R-1 — the container can strip the privilege while the host looks correct
- ADR-0003 — the system is deployed as a container on hyperion
- ADR-0005 — the configurable setuid-root mechanism this decision replaces
- Simplicity — the operator configures a MAC address, not a command

## Considered Options

1. Keep ADR-0005: a host setuid-root wake command, bind-mounted.
2. Fixed `etherwake`, installed in the image with a file capability
   `cap_net_raw+ep`, invoked directly with `-i <interface> <mac>` (chosen).
3. `sudo` plus a `sudoers` rule for `etherwake` (the user's first idea).
4. A wrapper script in the image that execs `etherwake` (raised, then dropped).
5. Unprivileged `wakeonlan` over UDP broadcast.

## Decision Outcome

Chosen option: "Fixed `etherwake` with a file capability", because it grants
only `CAP_NET_RAW` instead of ADR-0005's full root, keeps the proxy user
unprivileged, removes the host bind-mount and its `nosuid` trap, needs no
`sudo`/`sudoers` surface, and reduces the wake configuration to the MAC address.
The privilege moves from a host file mode to an extended attribute on an image
file, so `setcap`/`getcap` is the audit tool instead of `ls -l`.

The image installs `etherwake` and `libcap2-bin` and runs
`setcap cap_net_raw+ep /usr/sbin/etherwake`. The wake trigger always executes
`/usr/sbin/etherwake -i <wake_interface> <wake_mac>` as an argv, with no shell
and no argument splitting: ADR-0005's execution model is retained, but the
command itself is no longer configuration. Two keys replace `wake_command`:

| Key | Required | Default | Meaning |
|---|---|---|---|
| `wake_mac` | yes | — | the target's MAC address for the magic packet |
| `wake_interface` | no | `eth0` | the interface `etherwake` sends on |

This changes IF-5's key set and §5.4's wake interface, so those SRS sections are
updated with this decision (see Forms of change below).

### Consequences

- Good: NFR-8 is met more tightly than under ADR-0005 — the wake tool receives
  only `CAP_NET_RAW`, not full root.
- Good: R-1's bind-mount mechanism is gone; the wake tool is an image file, so a
  host `nosuid` filesystem cannot disable it.
- Good: no `sudo`, no `sudoers`, no wrapper, and no passwordless rule to
  mis-scope; the operator sets a MAC address and, if the default is wrong, an
  interface.
- Bad: the risk does not vanish, it changes shape — a runtime that drops file
  capabilities (for example with `no-new-privileges`, or by not preserving
  xattrs) breaks the wake path. R-1 is restated around `getcap`, not deleted.
- Bad: the wake mechanism is no longer pluggable. A non-`etherwake` mechanism
  now needs a code change, where ADR-0005 allowed a different configured
  command. This is the accepted cost of the simplification the user asked for.
- Bad: the image must keep `etherwake` patched, and the target's interface and
  MAC become tcp-wake configuration rather than a wake-script detail.

## Pros and Cons of the Options

### Keep ADR-0005 (host setuid-root command)

- Good, because the boundary is one host file mode, readable with `ls -l`.
- Good, because it supports any host wake command, not only `etherwake`.
- Bad, because a `nosuid` bind mount or a stripping runtime makes the bit inert
  while the host mode still reads `4755` (R-1).
- Bad, because every host must author and mount the command.

### Fixed `etherwake` with a NET_RAW file capability

- Good, because it grants the narrow capability the tool actually uses.
- Good, because the proxy stays a non-root user and there is no privileged
  intermediary such as `sudo`.
- Good, because the tool and its privilege travel with the image, so a redeploy
  is the same on every host, and configuration is one MAC address.
- Bad, because file capabilities depend on the runtime preserving xattrs and on
  the capability being in the container's bounding set.
- Bad, because replacing the wake mechanism now requires a code change.

### `sudo` plus a `sudoers` rule

- Good, because the rule can name the exact command and arguments, and sudo logs
  each invocation.
- Bad, because it widens the boundary to `sudo` and its configuration, which
  ADR-0005 already rejected.
- Bad, because `sudo` is itself setuid-root, so R-1's setuid concern remains
  (now on `/usr/bin/sudo`) rather than being removed.
- Bad, because a wildcard rule is easy to mis-scope, and the mistake is
  invisible to `ls -l`.

### A wrapper script in the image

- Good, because the MAC and interface could live in the wrapper instead of
  tcp-wake's configuration.
- Bad, because it adds a second moving part for no functional gain once the
  trigger can pass `-i` and the MAC itself; the user asked for the wrapper to be
  removed.

### Unprivileged `wakeonlan` over UDP

- Good, because UDP broadcast may need no capability at all, so nothing is
  privileged.
- Bad, because it sends to the broadcast address rather than a chosen
  interface's link layer, which is less reliable across hyperion's network.
- Bad, because it does not satisfy C-4 if the real wake path on hyperion is a
  raw-socket tool.

## Confirmation

`scripts/check-deploy.sh` verifies the capability from inside the running
container, as the unprivileged user: `getcap /usr/sbin/etherwake` must report
`cap_net_raw=ep`, and running `etherwake` as uid 10001 must not fail. The
differential was measured while deciding, on `debian:stable-slim`:

- with `cap_net_raw=ep`: `etherwake -i lo 00:11:22:33:44:55` exits 0;
- without it: it prints `etherwake: This program must be run as root.` and exits
  2.

NFR-7's inspection is therefore `getcap` plus that execution, replacing the
`ls -l` setuid check. `TestNFR7TriggerExecsConfiguredFile` and the FR-3 tests
continue to cover the trigger's half, with the trigger now exec'ing the fixed
`etherwake` argv.

## Forms of change

This ADR changes the SRS baseline, so the change control note in `SRS.md`
applies. The affected items are:

- **IF-5** — the configuration key set: `wake_command` is replaced by `wake_mac`
  (required) and `wake_interface` (default `eth0`).
- **§5.4 (wake interface)** — the command is fixed to `etherwake`; the MAC and
  interface are configuration.
- **§5.5 (configuration interface)** — the key list.
- **NFR-7** — the check changes from a setuid file mode to a file capability.
- **R-1 / §11** — restated around a dropped or ignored file capability.
- **Coverage appendix** — the wake requirements point at ADR-0016, not
  ADR-0005.

The traceability check is re-run after the change.

## Links

- Requirements: C-4, NFR-7, NFR-8, R-1, IF-5
- Related ADRs: ADR-0003 (the container this lives in), ADR-0005 (superseded),
  ADR-0011 (where `wake_mac`/`wake_interface` are configured)
- Architecture document: §7 Deployment View, §8.2 Configuration, §8.3 Privilege,
  §11 R-1
