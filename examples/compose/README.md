# Production compose reference

**Operators:** pull GHCR images and follow
[`RUNBOOK.md`](RUNBOOK.md) (OpenBao init/unseal, SQL seed, volume chmod).
Skill: [`reksicherung-bootstrap`](../../.agents/skills/reksicherung-bootstrap/SKILL.md).

Human onboarding: [`docs/getting-started.md`](../../docs/getting-started.md).  
Full AI input registry: [`docs/inputs.md`](../../docs/inputs.md).

## What this stack provides

| Service | Purpose |
| --- | --- |
| `openbao` | Persistent file-storage OpenBao (not `-dev`) |
| `sqlserver` | Isolated restore target (host `127.0.0.1:11433` → `1433`) |
| `mysql` (profile `mysql`) | Opt-in isolated MySQL restore target for logical `.sql`/`.sql.gz` dumps (host `127.0.0.1:13306` → `3306`); needs `MYSQL_ROOT_PASSWORD` |
| `cleanup-reconciler` | Durable drop of expired automatic run-scoped databases (`cli` image) |
| `*-mcp` (profile `mcp-stdio`) | Optional long-lived containers; preferred pattern is `docker run -i` — see [`docs/mcp-clients.md`](../../docs/mcp-clients.md) |

Images default to `ghcr.io/1k-off/reksicherung/*:${REKSICHERUNG_IMAGE_TAG:-latest}`.
`build:` blocks exist for **developers only** (`compose build`).

Per-backup S3/Restic credentials are stored in OpenBao
(`secret/reksicherung/sources/<source_id>`), not in `.env`.
SQL connection (`HOST`/`USERNAME`/`PASSWORD`, optional `PORT`) is
`secret/reksicherung/sqlserver`; MySQL (`database.type: mysql`) uses
`secret/reksicherung/mysql` (optional `PORT`, default `3306`).

MCP services in profile `mcp-stdio`: `gobackup-mcp`, `restic-mcp`,
`sqlserver-restore-mcp`, `sqlserver-inspect-mcp`.

**MySQL is opt-in (profile `mysql`).** `mysql`, `mysql-restore-mcp`
(`mcp-mysql-restore`) and `mysql-inspect-mcp` (`mcp-mysql-inspect`) are not
started by plain `docker compose up` (so an empty `MYSQL_ROOT_PASSWORD` cannot
break SQL Server-only stacks). Enable with `--profile mysql`, e.g.
`docker compose --env-file .env --profile mysql up -d mysql` (add the MCP
service names only if you want long-lived stdio containers). MySQL restore needs
only the shared `artifact-stage` volume (dumps are streamed; no `sql-backup`
volume).

## Bootstrap

```bash
cp .env.example .env
# edit bootstrap secrets only (compose-env.schema.json)
# pull GHCR — do not build — then follow RUNBOOK.md
```

Preferred production pattern: an MCP host runs MCP images as stdio children
with OpenBao bootstrap env only; tools receive `source_id` per call.

- Any MCP host + GHCR: [`docs/mcp-clients.md`](../../docs/mcp-clients.md)
- Optional Airon lab pack: skill `reksicherung-airon-onboard`
- Host-binary Airon stub only: [`../airon/README.md`](../airon/README.md)

## Safety

- Do not publish SQL Server or MySQL ports on the host in production.
- Pin image digests before promotion (`latest` is lab-only).
- Replace OpenBao HTTP with TLS before non-loopback use.
- `hold_for_review` databases are never auto-dropped.
- Set `REKSICHERUNG_ARTIFACT_STAGE_DIR` to the literal
  `/var/lib/reksicherung/artifacts` (never a Windows host path).
