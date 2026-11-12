# Production compose reference

See [`docs/inputs.md`](../../docs/inputs.md) for the full AI input registry.

## What this stack provides

| Service | Purpose |
| --- | --- |
| `openbao` | Persistent file-storage OpenBao (not `-dev`) |
| `sqlserver` | Isolated restore target |
| `cleanup-reconciler` | Durable drop of expired automatic run-scoped databases |
| `*-mcp` (profile `mcp-stdio`) | Optional containers when an orchestrator launches MCP via `docker run -i` |

Per-backup S3/Restic credentials are stored in OpenBao
(`secret/reksicherung/sources/<source_id>`), not in `.env`.
SQL connection (`HOST`/`USERNAME`/`PASSWORD`, optional `PORT`) is
`secret/reksicherung/sqlserver`.

## Bootstrap

```bash
cp .env.example .env
# edit bootstrap secrets only (compose-env.schema.json)

docker compose build
docker compose up -d openbao sqlserver

# Init/unseal OpenBao; enable KV v2; write defaults + sqlserver + per-source KV
# (docs/schemas/openbao-source-kv.md). Put least-privilege token into .env.

docker compose up -d cleanup-reconciler
```

Preferred production pattern: an MCP host runs MCP binaries as stdio children
with OpenBao bootstrap env only; tools receive `source_id` per call. Manual
operation and other orchestrators are supported. Optional Airon example:
`../airon/`.

## Safety

- Do not publish SQL Server ports on the host in production.
- Pin image digests before promotion.
- Replace OpenBao HTTP with TLS before non-loopback use.
- `hold_for_review` databases are never auto-dropped.
