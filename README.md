# Reksicherung

Automated backup restoreability checks for SQL Server backups (GoBackup and Restic) stored in S3-compatible object storage.

Reksicherung is a set of MCP servers, an Airon skill, catalog utilities, and an optional GoBackup config generator. It does **not** create, start, or remove containers. Operators provision MCP services and the SQL Server restore target with Docker Compose (or equivalent); Airon schedules and drives the multi-step restore-check workflow.

## Components

| Binary | Role |
| --- | --- |
| `reksicherung` | Validate catalogs and emit secret-free Airon inventory JSON |
| `reksicherung-mcp-gobackup` | Discover/stage GoBackup archives from S3 |
| `reksicherung-mcp-restic` | Discover/stage files from a Restic repository |
| `reksicherung-mcp-sqlserver-restore` | Restore staged `.bak`/`.bacpac` into an operator-managed SQL Server; integrity check; drop run-scoped DB |
| `reksicherung-mcp-sqlserver` | Bounded read-only schema/query tools for AI semantic review |

Module path: `github.com/1k-off/reksicherung`.

## Operator flow

1. Describe backup sources in a `BackupCatalog` YAML (secret **references** only; see [`examples/catalog.yaml`](examples/catalog.yaml)).
2. Start services from [`examples/compose/docker-compose.yml`](examples/compose/docker-compose.yml): OpenBao, SQL Server, shared staging/backup volumes, MCP processes.
3. Point Airon at the MCP servers and the skill in [`skills/reksicherung-restore-check/SKILL.md`](skills/reksicherung-restore-check/SKILL.md).
4. Schedule Airon. It stages a backup, restores into a run-scoped database, runs DBCC, performs bounded semantic review, records evidence, and drops the database unless `hold_for_review`.

## Development

```bash
go test ./...
go vet ./...
```

Integration tests that need live SQL Server / S3 / OpenBao use the `integration` build tag and operator-started services; they never mutate source backup objects.

## Spec and decisions

- [`SPEC.md`](SPEC.md) — product boundaries and acceptance criteria
- [`docs/decisions/`](docs/decisions/) — catalog/inventory schema decisions
- [`docs/airon-integration-follow-up.md`](docs/airon-integration-follow-up.md) — optional Airon correlation/`_meta` improvements (Airon repo only)
