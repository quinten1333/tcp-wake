#!/usr/bin/env bash
#
# check-deploy.sh — verify the wake command's setuid boundary inside the
# running container (R-1, NFR-7, NFR-8, ADR-0003, ADR-0005).
#
# T1 proved the setuid bit survives a Docker bind mount by *executing* the
# binary and reading its effective uid; `stat` alone cannot prove the bit is not
# inert. This script makes that proof repeatable after any redeploy, so the
# wake path cannot regress silently while the host file mode still reads 4755.
#
# It exits non-zero if any of these fail:
#   1. the wake command's mode/owner inside the container is not 4755 root:root
#   2. the proxy process is not a non-root uid
#   3. executing the wake command does not yield effective uid 0
#
# Usage:
#   scripts/check-deploy.sh [--image IMAGE] [--wake-command PATH] [--no-build]
#
# Default: builds the image, creates a tiny setuid-root stub as the wake
# command, runs the container, checks, and cleans up.
#
# The stub MUST live on a filesystem that is not mounted nosuid (/tmp usually
# is). By default the script builds it under $HOME; override with
# TCPWAKE_CHECK_WORKDIR.

set -euo pipefail

IMAGE="tcp-wake:deploy-check"
WAKE_COMMAND=""
DO_BUILD=1
CONTAINER="tcp-wake-deploy-check"

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --image) IMAGE="$2"; shift 2 ;;
        --wake-command) WAKE_COMMAND="$2"; shift 2 ;;
        --no-build) DO_BUILD=0; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

fail() { echo "DEPLOY CHECK FAILED: $*" >&2; exit 1; }

cleanup() {
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    if [ -n "${WORKDIR:-}" ] && [ -d "$WORKDIR" ]; then
        rm -rf "$WORKDIR"
    fi
}
trap cleanup EXIT

# --- pick a non-nosuid workdir for the stub -------------------------------
WORKDIR="${TCPWAKE_CHECK_WORKDIR:-}"
if [ -z "$WORKDIR" ]; then
    WORKDIR="$(mktemp -d "${HOME:?HOME must be set}/tcp-wake-deploy-check.XXXXXX")"
fi
mkdir -p "$WORKDIR"

# --- build the image if asked ---------------------------------------------
if [ "$DO_BUILD" = 1 ]; then
    echo "==> building $IMAGE"
    docker build -q -t "$IMAGE" . >/dev/null || fail "docker build failed"
fi
docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image $IMAGE not found (drop --no-build or build it first)"

# --- the wake command to test ---------------------------------------------
if [ -z "$WAKE_COMMAND" ]; then
    WAKE_COMMAND="$WORKDIR/wol-send"
    cat > "$WORKDIR/wol-send.c" <<'EOF'
#include <stdio.h>
#include <unistd.h>
int main(void) {
    printf("real_uid=%d effective_uid=%d\n", (int)getuid(), (int)geteuid());
    return 0;
}
EOF
    cc -o "$WAKE_COMMAND" "$WORKDIR/wol-send.c" || fail "cannot compile the setuid stub"
    # chown needs root; the operator runs this after a deploy, where sudo is
    # available. Without root the setuid bit would be meaningless. chown clears
    # the setuid bit, so it must run BEFORE chmod, and both need root once the
    # file is owned by root.
    if [ "$(id -u)" = 0 ]; then
        chown root:root "$WAKE_COMMAND"
        chmod 4755 "$WAKE_COMMAND"
    else
        sudo chown root:root "$WAKE_COMMAND" || fail "cannot chown the stub; run with sudo or as root"
        sudo chmod 4755 "$WAKE_COMMAND" || fail "cannot chmod the stub; run with sudo or as root"
    fi

    # A bind mount inherits the source filesystem's options. Refuse to run on a
    # nosuid source: the result would be a false negative the operator might
    # mistake for a runtime problem.
    opts="$(findmnt -T "$WAKE_COMMAND" -no OPTIONS || true)"
    case " $opts " in
        *" nosuid "*) fail "$WAKE_COMMAND is on a nosuid filesystem; build it under \$HOME (or set TCPWAKE_CHECK_WORKDIR)" ;;
    esac
fi

# --- a minimal config so the proxy starts ---------------------------------
CONFIG="$WORKDIR/config.toml"
cat > "$CONFIG" <<'EOF'
listen_address = "127.0.0.1:18080"
target_address = "http://127.0.0.1:19999"
wake_command   = "/usr/local/bin/wol-send"
probe_interval = "2s"
probe_timeout  = "1s"
wait_bound     = "120s"
held_body_cap  = "64MiB"
EOF

# --- run the container as the unprivileged proxy user ---------------------
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
    --network none \
    --user 10001:10001 \
    -v "$WAKE_COMMAND:/usr/local/bin/wol-send:ro" \
    -v "$CONFIG:/etc/tcp-wake/config.toml:ro" \
    "$IMAGE" >/dev/null || fail "docker run failed"

# Wait briefly for the process to be up so exec runs in the running container.
for _ in $(seq 1 50); do
    state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)"
    [ "$state" = "true" ] && break
    sleep 0.1
done
[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ] \
    || fail "the tcp-wake container did not stay running; check its config"

echo "==> 1/3 wake-command mode and owner inside the container"
mode_owner="$(docker exec "$CONTAINER" stat -c '%a %U:%G' /usr/local/bin/wol-send)"
echo "    $mode_owner"
[ "$mode_owner" = "4755 root:root" ] || fail "wake command is not 4755 root:root inside the container (got: $mode_owner)"

echo "==> 2/3 proxy process uid"
uid="$(docker exec "$CONTAINER" id -u)"
echo "    uid=$uid"
[ "$uid" != "0" ] || fail "the proxy process runs as root (NFR-8)"

echo "==> 3/3 setuid bit is effective, not just displayed"
out="$(docker exec "$CONTAINER" /usr/local/bin/wol-send)"
echo "    $out"
effective="$(printf '%s' "$out" | sed -n 's/.*effective_uid=\([0-9]\+\).*/\1/p')"
[ -n "$effective" ] || fail "could not read the stub's effective uid from: $out"
[ "$effective" = "0" ] || fail "the setuid bit is inert: effective_uid=$effective (expected 0). Check for a nosuid mount or a runtime that strips setuid (R-1)."

echo "DEPLOY CHECK PASSED: setuid boundary intact (mode 4755 root:root, proxy uid $uid, effective_uid 0)"
