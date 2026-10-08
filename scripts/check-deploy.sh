#!/usr/bin/env bash
#
# check-deploy.sh — verify the wake tool's privilege boundary inside the running
# container (R-1, NFR-7, NFR-8, ADR-0003, ADR-0016).
#
# The wake tool is etherwake, and its privilege is a CAP_NET_RAW file
# capability on /usr/sbin/etherwake rather than a setuid bit. A container
# runtime can drop or ignore file capabilities (for example no-new-privileges,
# or a storage driver that does not preserve xattrs), so this check runs after
# every redeploy. It exits non-zero if any of these fail:
#
#   1. getcap does not report cap_net_raw=ep on /usr/sbin/etherwake
#   2. the proxy process is not a non-root uid
#   3. executing etherwake as the proxy user fails at the raw socket
#
# Usage:
#   scripts/check-deploy.sh [--image IMAGE] [--no-build]
#
# Default: builds the image, runs the container, checks, and cleans up.

set -euo pipefail

IMAGE="tcp-wake:deploy-check"
DO_BUILD=1
CONTAINER="tcp-wake-deploy-check"

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --image) IMAGE="$2"; shift 2 ;;
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

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/tcp-wake-deploy-check.XXXXXX")"

# --- build the image if asked ---------------------------------------------
if [ "$DO_BUILD" = 1 ]; then
    echo "==> building $IMAGE"
    # --network=host so the apt layer can reach the mirrors even where the
    # Docker bridge is unavailable.
    docker build --network=host -q -t "$IMAGE" . >/dev/null || fail "docker build failed"
fi
docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image $IMAGE not found (drop --no-build or build it first)"

# --- a minimal config so the proxy starts ---------------------------------
# wake_mac is required (ADR-0016); the interface is set to lo so check 3 below
# can run etherwake without touching a real NIC.
CONFIG="$WORKDIR/config.toml"
cat > "$CONFIG" <<'EOF'
listen_address = "127.0.0.1:18080"
target_address = "http://127.0.0.1:19999"
health_path    = "/health"
probe_interval = "2s"
probe_timeout  = "1s"
wait_bound     = "120s"
wake_mac       = "00:11:22:33:44:55"
wake_interface = "lo"
held_body_cap  = "64MiB"
EOF

# --- run the container as the unprivileged proxy user ---------------------
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
    --network none \
    --user 10001:10001 \
    -v "$CONFIG:/etc/tcp-wake/config.toml:ro" \
    "$IMAGE" >/dev/null || fail "docker run failed"

for _ in $(seq 1 50); do
    state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)"
    [ "$state" = "true" ] && break
    sleep 0.1
done
[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ] \
    || fail "the tcp-wake container did not stay running; check its config"

echo "==> 1/3 wake tool's file capability inside the container"
caps="$(docker exec "$CONTAINER" getcap /usr/sbin/etherwake)"
echo "    $caps"
case "$caps" in
    *cap_net_raw=ep*) ;;
    *) fail "etherwake does not carry cap_net_raw=ep inside the container (got: ${caps:-none}). Check for no-new-privileges or a runtime that drops file capabilities (R-1)." ;;
esac

echo "==> 2/3 proxy process uid"
uid="$(docker exec "$CONTAINER" id -u)"
echo "    uid=$uid"
[ "$uid" != "0" ] || fail "the proxy process runs as root (NFR-8)"

echo "==> 3/3 the capability is effective, not just displayed"
# With the capability this opens the raw socket and exits 0 (the packet on lo
# is harmless); without it etherwake prints 'must be run as root' and exits 2.
set +e
out="$(docker exec "$CONTAINER" /usr/sbin/etherwake -i lo 00:11:22:33:44:55 2>&1)"
rc=$?
set -e
echo "    rc=$rc ${out:-}"
[ "$rc" = "0" ] || fail "executing etherwake as the proxy user failed (rc=$rc): ${out:-no output}. The CAP_NET_RAW file capability is not effective (R-1)."

echo "DEPLOY CHECK PASSED: wake privilege intact (cap_net_raw=ep, proxy uid $uid, etherwake rc=0)"
