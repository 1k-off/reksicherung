# Reksicherung

Automated backup restoreability checks for SQL Server backups (GoBackup and Restic) stored in S3-compatible object storage or Azure Blob.

Reksicherung is a set of MCP servers, skills, catalog utilities, GoBackup/resticprofile config generators, and a durable database-cleanup reconciler. It does **not** create, start, or remove containers. Operators provision MCP services and the SQL Server restore target. Any MCP-capable orchestrator (for example Airon — see `examples/airon/`) or a human operator can drive the restore-check workflow. Airon is not a dependency.

Module path: `github.com/1k-off/reksicherung`. Catalog API: `reksicherung.1node.xyz/v1alpha2`.

## Start here

| You want… | Go to |
| --- | --- |
| First successful restore-check (no Airon) | **[`docs/getting-started.md`](docs/getting-started.md)** |
| Terms (`source_id`, catalog, verdicts, …) | [`docs/glossary.md`](docs/glossary.md) |
| Copy-paste OpenBao + SQL compose steps | [`examples/compose/RUNBOOK.md`](examples/compose/RUNBOOK.md) |
| Cursor / Claude / any MCP host + GHCR | [`docs/mcp-clients.md`](docs/mcp-clients.md) |
| Stack bootstrap (GHCR pull) | [`.agents/skills/reksicherung-bootstrap/SKILL.md`](.agents/skills/reksicherung-bootstrap/SKILL.md) |
| Airon lab pack (GHCR) | [`.agents/skills/reksicherung-airon-onboard/SKILL.md`](.agents/skills/reksicherung-airon-onboard/SKILL.md) |
| AI agent skill map | [`.agents/skills/reksicherung-index/SKILL.md`](.agents/skills/reksicherung-index/SKILL.md) |

**AI-first contracts:** all expected inputs are in [`docs/inputs.md`](docs/inputs.md) with schemas under [`docs/schemas/`](docs/schemas/).

## Components

| Binary | Role |
| --- | --- |
| `reksicherung` | Validate catalogs, emit inventory, render configgen YAML, cleanup held DBs, reconcile expired cleanup leases |
| `reksicherung-mcp-gobackup` | Discover/stage GoBackup archives per `source_id` (OpenBao) |
| `reksicherung-mcp-restic` | Discover/stage files from Restic repos per `source_id` (OpenBao) |
| `reksicherung-mcp-sqlserver-restore` | Restore staged `.bak`/`.bacpac`; integrity check; drop run-scoped DB |
| `reksicherung-mcp-sqlserver` | Bounded read-only schema/query tools for AI semantic review |

GHCR images (lab tag `latest`; pin digests for production):
`ghcr.io/1k-off/reksicherung/{mcp-gobackup,mcp-restic,mcp-sqlserver-restore,mcp-sqlserver-inspect,cli}:<TAG>`.

## Operator journey (summary)

1. Compose stack: OpenBao + SQL + reconciler — [`examples/compose/RUNBOOK.md`](examples/compose/RUNBOOK.md).
2. **Optional** backup-as-code: templates in [`examples/configgen/`](examples/configgen/), then `reksicherung configgen` ([`reksicherung-configgen`](.agents/skills/reksicherung-configgen/SKILL.md)).
3. Thin catalog + OpenBao `sources/<id>` ([`examples/catalog.example.yaml`](examples/catalog.example.yaml), [`reksicherung-add-source`](.agents/skills/reksicherung-add-source/SKILL.md)).
4. Attach MCP ([`docs/mcp-clients.md`](docs/mcp-clients.md) or Airon) and dry-run ([`reksicherung-restore-check`](.agents/skills/reksicherung-restore-check/SKILL.md)).
5. Recurring coverage after PASS ([`reksicherung-schedule-verify`](.agents/skills/reksicherung-schedule-verify/SKILL.md)).
6. Day-2: [`docs/operations.md`](docs/operations.md), [`reksicherung-operate`](.agents/skills/reksicherung-operate/SKILL.md).

## Operate

- [`docs/operations.md`](docs/operations.md) — production checklist, OpenBao, cleanup, evidence retention
- [`docs/evidence.md`](docs/evidence.md) — evidence JSONL contract
- [`examples/compose/`](examples/compose/) — reference stack

## Design

- [`docs/decisions/`](docs/decisions/) — ADRs
- [`docs/inputs.md`](docs/inputs.md) — input registry for AI agents

`docs/superpowers/` is internal planning, not operator onboarding.

## Development

```bash
go test ./...
go vet ./...
```

Build images only when iterating on this repo (operators must pull GHCR instead):

```bash
REKSICHERUNG_IMAGE_TAG=local docker compose -f examples/compose/docker-compose.yml build
```

E2E (operator fixtures): `go test -tags=e2e ./test/e2e/...` — see [`test/e2e/README.md`](test/e2e/README.md).
