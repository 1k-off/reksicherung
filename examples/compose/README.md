# Production compose reference

## What this stack provides

| Service | Purpose |
| --- | --- |
| `openbao` | Persistent file-storage OpenBao (not `-dev`) |
| `sqlserver` | Isolated restore target |
| `cleanup-reconciler` | Durable drop of expired automatic run-scoped databases |
| `*-mcp` (profile `mcp-stdio`) | Optional containers when Airon launches MCP via `docker run -i` |

Airon usually runs MCP binaries as stdio children. Preferred production pattern:

1. `docker compose up -d openbao sqlserver cleanup-reconciler`
2. Build MCP images once, or install binaries on the Airon host
3. Point Airon at the binaries / `docker run --rm -i --network reksicherung_reksicherung ...`

## Bootstrap

```bash
cp .env.example .env
# edit secrets and storage coordinates

docker compose build
docker compose up -d openbao sqlserver

# Initialize OpenBao (first boot only), unseal, enable KV v2 at mount `secret`,
# write referenced keys, then put a long-lived least-privilege token into .env.
# See docs/operations.md.

# Create dedicated SQL restore login with dbcreator + CHECKDB rights only on
# this instance. Never reuse production credentials.

docker compose up -d cleanup-reconciler
```

## Shared volumes

| Volume | Path in containers |
| --- | --- |
| `artifact-stage` | `/var/lib/reksicherung/artifacts` |
| `sql-backup` | `/var/opt/mssql/backup` |
| `sql-data` | `/var/opt/mssql/data` |
| `run-registry` | `/var/lib/reksicherung/runs` |
| `openbao-data` | `/vault/file` |

Provision ownership so MCP and SQL Server identities can read/write shared
paths. Processes never `chmod` mounts at runtime.

## Safety

- Do not publish SQL Server ports on the host in production.
- Pin image digests before promotion.
- Replace OpenBao HTTP with TLS termination / HTTPS listener before exposing
  beyond localhost.
- `hold_for_review` databases are never auto-dropped; use
  `reksicherung cleanup -registry ... -run-id ...`.
