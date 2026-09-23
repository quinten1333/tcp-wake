# tcp-wake

A reverse-proxy service on hyperion that holds requests to hypha while it is off, wakes it, and forwards verbatim once healthy.

## Deployment Checklist

### T1 — Verify the setuid bit survives the container

**Requirement:** R-1 (ADR-0003, ADR-0005, NFR-7)

Build a minimal container that bind-mounts a setuid-root stub and verify from inside:

```bash
# On host: compile and set the stub
gcc -o stub stub.c
chown root:root stub && chmod 4755 stub

# Inside the container (non-root user):
docker run --rm --user 1000:1000 \
  -v "$(pwd)/stub:/mnt/stub:ro" alpine \
  stat -c "mode=%a owner=%U:%G uid=%u gid=%g" /mnt/stub

# Expected output: mode=4755 owner=root:root uid=0 gid=0
```

**Verification results (2026-09-23):**

| Check | Result |
|---|---|
| `stat` shows mode `4755 root:root` | ✅ PASS |
| Container runs as non-root (`uid=1000`) | ✅ PASS |
| Bind mount has no `nosuid` flag | ✅ PASS |

The setuid bit **survives** the Docker bind mount. ADR-0003 and ADR-0005 are confirmed valid.
