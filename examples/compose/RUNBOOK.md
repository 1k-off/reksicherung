# Compose stack runbook (no Airon required)

Human copy-paste for `examples/compose/`. Skills and AI prompts should point here
instead of inventing OpenBao/SQL steps.

**Related:** [`docs/getting-started.md`](../../docs/getting-started.md) ·
[`docs/schemas/openbao-source-kv.md`](../../docs/schemas/openbao-source-kv.md) ·
[`.env.example`](.env.example)

Compose project name is `reksicherung` (see `name:` in `docker-compose.yml`):

| Resource | Name |
| --- | --- |
| Network | `reksicherung_reksicherung` |
| Volumes | `reksicherung_sql-backup`, `reksicherung_artifact-stage`, `reksicherung_sql-data`, `reksicherung_openbao-data`, `reksicherung_run-registry` |

Set `RUNTIME` to `docker`, `podman`, or `nerdctl`. Examples use `docker compose`.

Password charset for `.env`: avoid `$` (compose interpolation), `&`, and `%`
(Windows env-file). Prefer a long alphanumeric password.

---

## 1. Env file

```bash
cd examples/compose
cp .env.example .env
# Edit MSSQL_SA_PASSWORD only for now. Leave REKSICHERUNG_OPENBAO_TOKEN
# as a placeholder until after init.
```

## 2. Start OpenBao

```bash
docker compose --env-file .env up -d openbao
# Wait until healthy / port 127.0.0.1:8200 answers
```

## 3. Init + unseal (first time only)

```bash
docker compose --env-file .env exec -T openbao \
  bao operator init -key-shares=1 -key-threshold=1 -format=json \
  > openbao-init.json

# Unseal (bash):
UNSEAL=$(python -c "import json;print(json.load(open('openbao-init.json'))['unseal_keys_b64'][0])")
docker compose --env-file .env exec -T openbao bao operator unseal "$UNSEAL"

# Root token into .env (bash):
TOKEN=$(python -c "import json;print(json.load(open('openbao-init.json'))['root_token'])")
# Set REKSICHERUNG_OPENBAO_TOKEN=$TOKEN in .env (do not commit .env or openbao-init.json)
```

PowerShell (unseal + token):

```powershell
$init = Get-Content .\openbao-init.json -Raw | ConvertFrom-Json
docker compose --env-file .env exec -T openbao bao operator unseal $init.unseal_keys_b64[0]
# Put $init.root_token into .env as REKSICHERUNG_OPENBAO_TOKEN=...
```

Lab tip: keep the root token only for bring-up; replace with a least-privilege
token/AppRole before production (see [`docs/operations.md`](../../docs/operations.md)).

After reboot: OpenBao seals again — re-run `bao operator unseal` with the saved key.

## 4. Enable KV v2

```bash
export BAO_ADDR=http://127.0.0.1:8200
export BAO_TOKEN=<REKSICHERUNG_OPENBAO_TOKEN from .env>
# Or: docker compose exec with BAO_TOKEN inside the container

docker compose --env-file .env exec -T \
  -e BAO_ADDR=http://127.0.0.1:8200 \
  -e BAO_TOKEN \
  openbao bao secrets enable -path=secret kv-v2
```

If `path already in use`, skip — mount already exists.

## 5. Start SQL Server + reconciler

Images: OpenBao and SQL Server are public. CLI/reconciler image is built from
this repo (or retag a GHCR `cli` image as `reksicherung/cli:local`).

```bash
# From repo root — build CLI used by cleanup-reconciler (and optional MCP profiles)
docker compose -f examples/compose/docker-compose.yml --env-file examples/compose/.env build cleanup-reconciler

docker compose -f examples/compose/docker-compose.yml --env-file examples/compose/.env \
  up -d openbao sqlserver cleanup-reconciler

# Restore MCP runs as uid 65532; volume is often root-owned after first SQL start
docker run --rm -v reksicherung_sql-backup:/var/opt/mssql/backup alpine \
  chmod 1777 /var/opt/mssql/backup
```

Host SQL port in this compose file: `127.0.0.1:11433` → container `1433`.
Inside the compose network always use `HOST=sqlserver` and port `1433`.

## 6. Seed OpenBao — SQL connection

Use the **same** password as `MSSQL_SA_PASSWORD` for lab SA-based restore, or a
dedicated login you create in the next step.

```bash
docker compose --env-file .env exec -T \
  -e BAO_ADDR=http://127.0.0.1:8200 \
  -e BAO_TOKEN \
  openbao bao kv put secret/reksicherung/sqlserver \
  HOST=sqlserver \
  PORT=1433 \
  USERNAME=sa \
  PASSWORD="$MSSQL_SA_PASSWORD"
```

Changing `.env` later does **not** change the SA password already stored in the
SQL data volume.

## 7. Optional: dedicated restore login

Prefer a non-SA login in anything beyond a disposable lab:

```bash
# From a host that can reach 127.0.0.1:11433 (sqlcmd or Azure Data Studio)
# Password must match OpenBao USERNAME/PASSWORD when you switch off sa.
```

```sql
CREATE LOGIN reksicherung_restore WITH PASSWORD = 'SameAsOpenBaoPassword_123';
ALTER SERVER ROLE dbcreator ADD MEMBER reksicherung_restore;
```

Then update OpenBao:

```bash
bao kv put secret/reksicherung/sqlserver \
  HOST=sqlserver PORT=1433 \
  USERNAME=reksicherung_restore \
  PASSWORD='SameAsOpenBaoPassword_123'
```

## 8. Seed OpenBao — defaults + one source

See field rules in [`docs/schemas/openbao-source-kv.md`](../../docs/schemas/openbao-source-kv.md).
`STORAGE_TYPE` is required (`s3` or `azure`).

S3 example:

```bash
bao kv put secret/reksicherung/defaults \
  STORAGE_TYPE=s3 \
  S3_ENDPOINT=https://objects.example.test \
  S3_REGION=eu-central-1 \
  S3_BUCKET=demo-backups

bao kv put secret/reksicherung/sources/demo-app-a \
  S3_ACCESS_KEY_ID=... \
  S3_SECRET_ACCESS_KEY=... \
  S3_PATH_PREFIX=demo-a/prod/
```

## 9. Catalog validate

```bash
# With a local Go toolchain from repo root:
go run ./cmd/reksicherung validate -config examples/catalog.example.yaml
go run ./cmd/reksicherung catalog -config examples/catalog.example.yaml

# Or GHCR CLI (mount your catalog):
docker run --rm -v "$PWD/examples:/examples:ro" \
  ghcr.io/1k-off/reksicherung/cli:latest \
  validate -config /examples/catalog.example.yaml
```

Replace the example `source_id`s with yours and keep OpenBao paths in sync.

## 10. MCP servers

Do **not** put per-backup keys in `.env`. MCP processes only need OpenBao
bootstrap env.

- **Any MCP host (Cursor, etc.):** [`docs/mcp-clients.md`](../../docs/mcp-clients.md)
- **Airon lab pack:** skill `reksicherung-airon-onboard`
- **Compose profile `mcp-stdio`:** long-lived containers for hosts that attach
  to running stdio services (less common than `docker run -i`)

Shared mounts required between provider + restore MCP:

- `reksicherung_artifact-stage` → `/var/lib/reksicherung/artifacts`
- `reksicherung_sql-backup` → `/var/opt/mssql/backup` (restore + SQL Server)

Always set `REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts`
as a **literal** (do not interpolate a Windows host path).

## 11. Day-2

| Task | Action |
| --- | --- |
| After reboot | Unseal OpenBao; `up -d` stack; re-`chmod 1777` sql-backup if volume recreated |
| hold_for_review drop | `reksicherung cleanup -registry … -run-id …` (skill `reksicherung-operate`) |
| Triage FAIL | Evidence + OpenBao source KV + inventory; see `docs/operations.md` |

## Tear down

```bash
docker compose --env-file .env --profile mcp-stdio down
# Add -v only when intentionally wiping OpenBao + SQL data
```
