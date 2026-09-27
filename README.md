# tcp-wake

A reverse-proxy service on hyperion that holds requests to hypha while it is off, wakes it, and forwards verbatim once healthy.

## Deployment Checklist

### T1 — Verify the setuid bit survives the container

**Requirement:** R-1 (ADR-0003, ADR-0005, NFR-7)

The wake command is a root-owned, setuid (`4755`) file. ADR-0003 deploys the
proxy in a container, which risks the runtime or storage driver stripping the
bit. This check proves the bit survives *and remains effective* inside a
container running as a non-root user.

> **Why the check must run from the repository directory.** `/tmp` is mounted
> `nosuid` on this host, and a bind mount inherits the source filesystem's
> mount options. A stub built under `/tmp` therefore loses its setuid effect
> even though `stat` still prints `4755`. Always build the stub on a
> non-`nosuid` filesystem (the repo checkout is on the btrfs root subvolume).
> The definitive test is **executing** the binary and reading its effective uid;
> `stat` alone cannot prove the bit is not inert.

Repeatable verification (run as the `arch` user, which is in the `docker` group):

```bash
cd "$(git rev-parse --show-toplevel)"

# 1. Build the stub on a non-nosuid filesystem.
cat > stub.c <<'EOF'
#include <stdio.h>
#include <unistd.h>
int main(void) {
    printf("real_uid=%d effective_uid=%d\n", (int)getuid(), (int)geteuid());
    return 0;
}
EOF
gcc -o stub stub.c
sudo chown root:root stub && sudo chmod 4755 stub
stat -c '%a %U:%G' stub        # -> 4755 root:root

# 2. Run it from inside a container as a non-root user.
sudo docker run --rm --user 1000:1000 \
  -v "$(pwd)/stub:/mnt/stub:ro" debian:stable-slim \
  sh -c "stat -c 'mode=%a owner=%U:%G' /mnt/stub; id; /mnt/stub; grep '/mnt/stub' /proc/mounts"

# Expected:
#   mode=4755 owner=root:root
#   uid=1000 gid=1000 groups=1000
#   real_uid=1000 effective_uid=0        <- the setuid bit is effective
#   /dev/vda3 /mnt/stub btrfs ro,...     <- no 'nosuid' in the options

# 3. Clean up.
rm -f stub.c stub
```

**Verification results (2026-09-23):**

| Check | Result |
|---|---|
| `stat` shows mode `4755 root:root` inside the container | ✅ PASS |
| Container process runs as non-root (`uid=1000 gid=1000`) | ✅ PASS |
| Executing the stub gives `real_uid=1000 effective_uid=0` (bit is effective) | ✅ PASS |
| Bind mount options contain no `nosuid` | ✅ PASS |

The setuid bit **survives and remains effective** through the Docker bind mount.
ADR-0003 and ADR-0005 are confirmed valid; NFR-7's privilege boundary holds in
the container. T2 may proceed.
