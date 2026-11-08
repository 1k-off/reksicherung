# Reksicherung

Automated backup restoreability checks for SQL Server backups (GoBackup and Restic) stored in S3-compatible object storage.

Reksicherung is a set of MCP servers, an Airon skill, catalog utilities, an optional GoBackup config generator, and a durable database-cleanup reconciler. It does **not** create, start, or remove containers. Operators provision MCP services and the SQL Server restore target; Airon schedules and drives the multi-step restore-check workflow.

## Components

| Binary | Role |
| --- | --- |
| `reksicherung` | Validate catalogs, emit Airon inventory, cleanup held DBs, reconcile expired cleanup leases |
| `reksicherung-mcp-gobackup` | Discover/stage GoBackup archives from S3 |
| `reksicherung-mcp-restic` | Discover/stage files from a Restic repository |
| `reksicherung-mcp-sqlserver-restore` | Restore staged `.bak`/`.bacpac` into an operator-managed SQL Server; integrity check; drop run-scoped DB |
| `reksicherung-mcp-sqlserver` | Bounded read-only schema/query tools for AI semantic review |

Module path: `github.com/1k-off/reksicherung`.

## Production deploy

1. Copy [`examples/compose/.env.example`](examples/compose/.env.example) → `.env` and set real secret references / storage coordinates.
2. Follow [`docs/operations.md`](docs/operations.md): OpenBao init/unseal, KV secrets, isolated SQL restore login, image digests.
3. Build and start: `docker compose -f examples/compose/docker-compose.yml build && docker compose -f examples/compose/docker-compose.yml up -d openbao sqlserver cleanup-reconciler`
4. Configure Airon with [`examples/airon/restore-check.yaml`](examples/airon/restore-check.yaml) and [`skills/reksicherung-restore-check/SKILL.md`](skills/reksicherung-restore-check/SKILL.md).
5. Keep evidence per [`docs/evidence.md`](docs/evidence.md).
6. Before enabling cron, run the release-gate E2E when fixtures are available: `go test -tags=e2e ./test/e2e/...`

## Development

```bash
go test ./...
go vet ./...
```

Integration/E2E tests that need live SQL Server / S3 / OpenBao use build tags (`integration`, `e2e`) and operator-started services; they never mutate source backup objects.

## Spec and decisions

- [`SPEC.md`](SPEC.md) — product boundaries and acceptance criteria
- [`docs/decisions/`](docs/decisions/) — catalog/inventory schema decisions
- [`docs/operations.md`](docs/operations.md) — production checklist
- [`docs/airon-integration-follow-up.md`](docs/airon-integration-follow-up.md) — optional Airon `_meta` / log handoff improvements (Airon repo only)
