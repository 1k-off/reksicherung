# OpenBao + Traefik (public TLS)

Production-style OpenBao for Reksicherung secrets: Traefik terminates TLS on a
**high port** (default **8443**), Let's Encrypt via **Cloudflare DNS-01**. Host
**80/443 stay free**.

Lab stack without public TLS: [`../compose/`](../compose/).

Do **not** commit `.env`, `openbao-init.json`, unseal keys, or ACME state.

## Prerequisites

| Need | Notes |
| --- | --- |
| DNS A/AAAA | `OPENBAO_HOST` → this server (in Cloudflare DNS) |
| `CF_DNS_API_TOKEN` | Cloudflare API token with Zone.DNS Edit on that zone |
| Firewall | inbound TCP `OPENBAO_HTTPS_PORT` (default 8443) |
| `OPENBAO_UI_ALLOWLIST` | CIDRs for `/` and `/ui` only; `/v1` is token-auth |

## 1. Env

```bash
cd examples/openbao-traefik
cp .env.example .env
# Set OPENBAO_HOST, TRAEFIK_ACME_EMAIL, CF_DNS_API_TOKEN, OPENBAO_UI_ALLOWLIST
```

## 2. Start

```bash
docker compose --env-file .env up -d
```

- Admin CLI / init: `http://127.0.0.1:8200` (loopback)
- Public: `https://$OPENBAO_HOST:$OPENBAO_HTTPS_PORT`

Wait until OpenBao is healthy before expecting TLS. If `:8200` was “address
already in use”, recreate: `docker compose --env-file .env up -d --force-recreate openbao`.

## 3. Init + unseal (once)

```bash
docker compose --env-file .env exec -T openbao \
  bao operator init -key-shares=1 -key-threshold=1 -format=json \
  > openbao-init.json

UNSEAL=$(python3 -c "import json;print(json.load(open('openbao-init.json'))['unseal_keys_b64'][0])")
docker compose --env-file .env exec -T openbao bao operator unseal "$UNSEAL"
export BAO_TOKEN=$(python3 -c "import json;print(json.load(open('openbao-init.json'))['root_token'])")
export BAO_ADDR=http://127.0.0.1:8200
```

PowerShell:

```powershell
$init = Get-Content .\openbao-init.json -Raw | ConvertFrom-Json
docker compose --env-file .env exec -T openbao bao operator unseal $init.unseal_keys_b64[0]
$env:BAO_TOKEN = $init.root_token
$env:BAO_ADDR = "http://127.0.0.1:8200"
```

## 4. KV v2 + AppRole

```bash
docker compose --env-file .env exec -T \
  -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN \
  openbao bao secrets enable -path=secret kv-v2
# skip if path already in use
```

Seed + per-source AppRole via CLI (admin token):

```bash
export REKSICHERUNG_OPENBAO_ADDR=http://127.0.0.1:8200
export REKSICHERUNG_OPENBAO_TOKEN="$BAO_TOKEN"

reksicherung openbao kv put -path reksicherung/sources/demo-app \
  STORAGE_TYPE=s3 S3_BUCKET=backups # …other fields

reksicherung openbao approle ensure -source-id demo-app
# copy role_id / secret_id to the backup host (mode 0600); never put root there
```

KV layout: [`docs/schemas/openbao-source-kv.md`](../../docs/schemas/openbao-source-kv.md).  
Ops notes: [`docs/operations.md`](../../docs/operations.md).

## 5. Disk auto-unseal (optional)

After reboot OpenBao seals until unsealed. Lab/early-prod option:

```bash
sudo mkdir -p /etc/openbao /usr/local/lib/reksicherung
sudo python3 -c "import json;print(json.load(open('openbao-init.json'))['unseal_keys_b64'][0])" \
  | sudo tee /etc/openbao/unseal.key >/dev/null
sudo chmod 600 /etc/openbao/unseal.key
sudo cp scripts/unseal.sh /usr/local/lib/reksicherung/openbao-unseal.sh
sudo chmod 755 /usr/local/lib/reksicherung/openbao-unseal.sh
sudo cp systemd/openbao-unseal.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now openbao-unseal.service
```

## Security

- Root/admin token: password manager only — not `.env`, not backup hosts
- UI allowlist ≠ API lockdown; API relies on tokens/policies
- Disk unseal key = convenience over HSM/KMS
