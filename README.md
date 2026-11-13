# Reksicherung

Automated backup restoreability checks for SQL Server backups (GoBackup and Restic) stored in S3-compatible object storage.

Reksicherung is a set of MCP servers, skills, catalog utilities, an optional GoBackup config generator, and a durable database-cleanup reconciler. It does **not** create, start, or remove containers. Operators provision MCP services and the SQL Server restore target. Any MCP-capable orchestrator (for example Airon — see `examples/airon/`) or a human operator can drive the restore-check workflow. Airon is not a dependency.

**AI-first:** all expected inputs are registered in [`docs/inputs.md`](docs/inputs.md) with machine-readable schemas under [`docs/schemas/`](docs/schemas/).

Module path: `github.com/1k-off/reksicherung`. Catalog API: `reksicherung.1node.xyz/v1alpha2`.

## Components

| Binary | Role |
| --- | --- |
| `reksicherung` | Validate catalogs, emit inventory, cleanup held DBs, reconcile expired cleanup leases |
| `reksicherung-mcp-gobackup` | Discover/stage GoBackup archives per `source_id` (OpenBao) |
| `reksicherung-mcp-restic` | Discover/stage files from Restic repos per `source_id` (OpenBao) |
| `reksicherung-mcp-sqlserver-restore` | Restore staged `.bak`/`.bacpac`; integrity check; drop run-scoped DB |
| `reksicherung-mcp-sqlserver` | Bounded read-only schema/query tools for AI semantic review |

## Production deploy

1. Follow skill [`skills/reksicherung-bootstrap/SKILL.md`](skills/reksicherung-bootstrap/SKILL.md).
2. Copy [`examples/compose/.env.example`](examples/compose/.env.example) → `.env` (bootstrap only).
3. OpenBao KV: defaults + `sources/<id>` + sqlserver ([`docs/schemas/openbao-source-kv.md`](docs/schemas/openbao-source-kv.md)).
4. Thin catalog ([`examples/catalog.example.yaml`](examples/catalog.example.yaml)); `reksicherung validate` / `catalog`.
5. Drive MCP tools manually or via an orchestrator job ([`examples/airon/restore-check.yaml`](examples/airon/restore-check.yaml) is one optional binding) + restore-check skill.
6. E2E when fixtures available: `go test -tags=e2e ./test/e2e/...`

## Development

```bash
go test ./...
go vet ./...
```

## Spec and decisions

- [`SPEC.md`](SPEC.md) — product boundaries
- [`docs/inputs.md`](docs/inputs.md) — input registry for AI agents
- [`docs/decisions/`](docs/decisions/) — ADRs
