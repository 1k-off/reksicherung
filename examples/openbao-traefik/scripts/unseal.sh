#!/usr/bin/env bash
# Unseal OpenBao from a disk key file. For systemd oneshot after compose start.
# Weaker than KMS — root on this host can unseal and read all KV.
set -euo pipefail

KEY_FILE="${OPENBAO_UNSEAL_KEY_FILE:-/etc/openbao/unseal.key}"
BAO_ADDR="${BAO_ADDR:-http://127.0.0.1:8200}"
MAX_WAIT_SECS="${OPENBAO_UNSEAL_WAIT_SECS:-120}"

log() { printf '[openbao-unseal] %s\n' "$*" >&2; }

if [[ ! -f "$KEY_FILE" ]]; then
  log "missing key file: $KEY_FILE"
  exit 1
fi
if [[ "$(stat -c '%a' "$KEY_FILE" 2>/dev/null || echo '?')" != "600" ]]; then
  log "warning: $KEY_FILE mode should be 0600"
fi

wait_ready() {
  local i=0
  while (( i < MAX_WAIT_SECS )); do
    if curl -sf "${BAO_ADDR}/v1/sys/seal-status" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    ((i++)) || true
  done
  log "OpenBao not reachable at ${BAO_ADDR} after ${MAX_WAIT_SECS}s"
  return 1
}

is_sealed() {
  python3 - "$BAO_ADDR" <<'PY'
import json, sys, urllib.request
addr = sys.argv[1].rstrip("/")
with urllib.request.urlopen(addr + "/v1/sys/seal-status", timeout=5) as r:
    print("yes" if json.load(r).get("sealed", True) else "no")
PY
}

do_unseal() {
  python3 - "$BAO_ADDR" "$KEY_FILE" <<'PY'
import json, sys, urllib.request
addr = sys.argv[1].rstrip("/")
key = open(sys.argv[2], encoding="utf-8").read().strip()
req = urllib.request.Request(
    addr + "/v1/sys/unseal",
    data=json.dumps({"key": key}).encode(),
    headers={"Content-Type": "application/json"},
    method="PUT",
)
with urllib.request.urlopen(req, timeout=30) as r:
    body = json.load(r)
if body.get("sealed", True):
    raise SystemExit("still sealed after unseal")
PY
}

wait_ready

if [[ "$(is_sealed)" == "no" ]]; then
  log "already unsealed"
  exit 0
fi

log "unsealing…"
do_unseal
log "unsealed OK"
