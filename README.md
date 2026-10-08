# tcp-wake

A reverse-proxy service on **hyperion** that holds requests to **hypha** while it
is powered off, runs `etherwake`, polls hypha's health endpoint, and forwards
each held request verbatim once hypha is ready. A client never sees an error
caused by hypha being off (except the one accepted case in [Accepted
risks](#accepted-risks-do-not-fix-these)).

- Requirement baseline: `docs/specs/SRS.md` v0.3
- Architecture and all 17 ADRs: `docs/architecture.md`, `docs/adr/`
- Requirement-to-test mapping: `docs/specs/RTM.md`

## How it works

```
client ──▶ existing routing ──▶ tcp-wake ──▶ hypha
              (TLS, auth)         │  hold, wake, probe, forward
                                  └── exec etherwake (CAP_NET_RAW)
```

The system is a single unprivileged Go process in a container with host
networking. While hypha is not healthy a request is held open with **no deadline
and no response bytes**; each held request triggers one `etherwake` execution.
A goroutine probes `GET /health` while a request waits. On the first ready
answer every held request opens its own upstream connection and is forwarded
byte-for-byte, streaming the response back. See `docs/architecture.md` §6 for
the runtime views.

## Logs

Three greppable text line forms on standard output:

```
2026-10-08T12:00:00Z wake command="/usr/sbin/etherwake -i eth0 AA:BB:CC:DD:EE:FF" status=0
2026-10-08T12:00:00Z health state=healthy
2026-10-08T12:00:00Z error status=504 component=wait_bound message="…"
```

One wake line per wake-command execution (the counting artefact for FR-3, FR-9,
and FR-12), one health line per target state **change** (not per probe, the
initial not-healthy state is not a change), and one error line per produced
error. No request or response content is written (ADR-0010, ADR-0017).

## Build and deploy

Prerequisites on hyperion: Docker with the Compose plugin, the target's MAC
address, and a reachable hypha address.

```bash
git clone <repo> && cd tcp-wake
docker compose build
docker compose up -d
```

`compose.yaml` (ADR-0003) runs the container with host networking and mounts the
configuration file next to it by default:

| Mount | Purpose |
|---|---|
| `./config.toml` → `/etc/tcp-wake/config.toml` | configuration, including `wake_mac` (ADR-0015, ADR-0016) |

Override the host path with `TCPWAKE_CONFIG_PATH`:

```bash
TCPWAKE_CONFIG_PATH=/srv/tcp-wake/config.toml docker compose up -d
```

The image installs `etherwake` and sets `cap_net_raw+ep` on it, so the wake tool
receives only the raw-socket capability rather than full root (ADR-0016).
`docker compose build` produces a static binary run as the non-root user
`tcpwake` (uid 10001). No capabilities are added to the container, and
`no-new-privileges` is deliberately **not** set because it would make the file
capability inert and break the wake path (NFR-7, R-1). Nothing is persisted; a
restart discards all held state (FR-16).

## The routing entry to add

Add exactly **one** route for hypha and change nothing else (ADR-0002, FR-9).
The route's upstream is `listen_address` from the config, over plain HTTP. TLS
termination and client authentication stay at the routing layer (ADR-0006,
§2 non-goal 6).

With an nginx-style layer:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;   # = listen_address
    proxy_http_version 1.1;
    proxy_buffering off;                # preserve streamed responses (FR-15)
}
```

Keep this route and `listen_address` in step (R-10). `listen_address` **must not**
be reachable from outside hyperion, or prompts travel in the clear (R-7).

## Configuration

TOML file, discovered by `--config`, then `$TCPWAKE_CONFIG`, then the fixed
default `/etc/tcp-wake/config.toml` (ADR-0015). Every key has an environment
override `TCPWAKE_<KEY>` that wins over the file (ADR-0011). `docs/config.example.toml`
is the annotated example and `config.toml.example` the short one.

| Key | Default | Meaning |
|---|---|---|
| `listen_address` | `127.0.0.1:8080` | address the routing layer forwards to |
| `target_address` | `http://hypha.lan:8080` | hypha, target of the probe and every forward |
| `health_path` | `/health` | path probed; ready is `200 {"status":"ok"}` (IF-3) |
| `probe_interval` | `2s` | probe cadence while a request is pending (ADR-0007) |
| `probe_timeout` | `1s` | per-probe timeout |
| `wait_bound` | `120s` | max hold, measured from arrival and never restarted (FR-8) |
| `wake_mac` | — (required) | target's MAC address; `etherwake` runs once per triggering request (FR-3) |
| `wake_interface` | — (optional) | interface `etherwake` sends on; unset omits `-i` and uses etherwake's default (ADR-0016) |
| `held_body_cap` | `64MiB` | max request body retained; over it the client gets 413 (FR-20) |

Durations are Go duration strings (`120s`, `500ms`); sizes accept an IEC suffix
(`64MiB`, `1GiB`). A missing, unreadable, or malformed file prevents start with
a message naming the offending key. An environment override set to empty is an
error, not a silent fallback.

## Verify a deployment

```bash
# 1. The wake privilege inside the running container (NFR-7, NFR-8, R-1).
scripts/check-deploy.sh

# 2. The requirement suite.
go test ./... && go test -race ./...

# 3. The spec/architecture traceability check.
python3 scripts/check_traceability.py docs/specs/SRS.md docs/architecture.md
```

### Inspection checklist — NFR-4 (no deadline on a held connection)

- [ ] `TestNFR4NoDeadlineCallsInSource` passes: no `SetDeadline`,
      `SetReadDeadline`, or `SetWriteDeadline` in the non-test proxy sources.
- [ ] `TestNFR4ListenerSetsNoDeadline` passes: a deadline spy sees no deadline.
- [ ] Manual: with hypha off, open a connection to `listen_address`, send a
      request, and confirm it receives zero bytes until hypha reports healthy.
      Do **not** add a connection timeout to "fix" a stuck request (non-goal 7).

### Inspection checklist — NFR-7 (the wake command gets its privilege)

- [ ] `scripts/check-deploy.sh` passes: inside the container `getcap` reports
      `cap_net_raw=ep` on `/usr/sbin/etherwake` and executing it as the proxy
      user exits 0.
- [ ] `docker exec tcp-wake getcap /usr/sbin/etherwake` prints
      `/usr/sbin/etherwake cap_net_raw=ep`.
- [ ] The container is **not** run with `no-new-privileges`, which would make
      the file capability inert.

### Inspection checklist — NFR-8 (nothing else is privileged)

- [ ] `docker exec tcp-wake id` shows a non-root uid (the image uses 10001).
- [ ] `compose.yaml` adds no capability, sets no `privileged: true`, and does
      not set `no-new-privileges` (which would disable the wake capability).
- [ ] `TestNFR8NoPrivilegeEscalationInSource` passes: the code never calls
      `Setuid`/`Setgid`/`Setgroups` or asks for a capability.

## Accepted risks (do not fix these)

These are decisions, not defects. A change that "fixes" one of them violates a
requirement or an ADR. The full risk table is `docs/architecture.md` §11.

| Accepted behaviour | Why it must stay |
|---|---|
| A held request has **no connection timeout** | NFR-4 and §2 non-goal 7; timeouts belong to hypha and the routing layer |
| A client that stalls mid-body can be held indefinitely (R-4) | Detecting it needs a timeout, which NFR-4 forbids |
| The held set is **uncapped** (ADR-0012) | NFR-5 requires at least 8; growth is bounded by the client population, and the body cap bounds each request |
| The first request after an out-of-band shutdown gets a **502** (R-8) | ADR-0008 trusts the cached health state instead of probing every forward |
| The system holds **nothing across a restart** (FR-16) | §2 non-goal 5; restart closes held connections rather than replaying them |
| Request/response bodies are **never logged** (ADR-0010) | §2 non-goal 3; logging them would write prompts and tokens to disk |
| hypha's idle-shutdown can race a wake (R-3) | The two hosts do not communicate; accepted, not mitigated |
| The wake command is **fixed to `etherwake`** (ADR-0016) | A different mechanism needs a code change; that is the accepted cost of reducing wake configuration to a MAC address |
| TLS and client authentication are **not** here | ADR-0006 and §2 non-goal 6; the routing layer is the trust boundary |

## Configuration notes you must not "optimise" away

- `wait_bound` is measured from the request's arrival and is **not restarted** by
  a wake attempt (FR-8).
- The probe runs **only** while a request is pending and on the first ready
  answer it stops; it never probes on a timer while idle (FR-12, ADR-0008).
- `wake_mac` is required and has no default; the wake command is fixed to
  `etherwake` (ADR-0016).
- `etherwake` is exec'd **directly with an argv**: no shell and no argument
  splitting, so a MAC address cannot be interpreted as shell input.
