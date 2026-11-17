# Getting started

Goal: one successful restore-check (`PASS`, `FAIL`, or documented `INCONCLUSIVE`
from real MCP errors) **without reading Go source**.

Glossary: [`glossary.md`](glossary.md) · Inputs: [`inputs.md`](inputs.md) ·
AI skill map: [`.agents/skills/reksicherung-index/SKILL.md`](../.agents/skills/reksicherung-index/SKILL.md)

## Choose a path

| Path | Use when | Start here |
| --- | --- | --- |
| **A. Lab / GHCR + any MCP host** | Pull published images; Cursor/Claude/other stdio (no Airon) | This page → skill [`reksicherung-bootstrap`](../.agents/skills/reksicherung-bootstrap/SKILL.md) → [RUNBOOK](../examples/compose/RUNBOOK.md) → [mcp-clients](mcp-clients.md) |
| **B. Airon pack** | Client/lab already uses Airon + LLM | Skill [`reksicherung-airon-onboard`](../.agents/skills/reksicherung-airon-onboard/SKILL.md) |
| **Developers only** | Changing this repo’s images/code | Bootstrap skill section “Developers only — build from source” |

Paths A and B both use **GHCR** (`latest` or a pinned tag). Do not `compose build` for operator bring-up.

## Ask once (decision checklist)

1. **Backup tool:** GoBackup or Restic?
   **Database engine:** SQL Server (`.bak`/`.bacpac`) or MySQL (logical `.sql`/`.sql.gz` dump)? Sets catalog `database.type` (`sqlserver` default, or `mysql`) and the OpenBao secret (`sqlserver` vs `mysql`).
2. **Storage:** S3-compatible or Azure? (`STORAGE_TYPE` required)
3. **Already have backups in object storage?**  
   - Yes → skip configgen; go to catalog + OpenBao source.  
   - No → optional [`reksicherung-configgen`](../.agents/skills/reksicherung-configgen/SKILL.md), then deploy YAML on the backup host, then register the source.
4. **Orchestrator:** Airon (path B) / other MCP host (path A) / manual tool calls (path A)?
5. **OS + container runtime:** Docker, Podman, or nerdctl?

## Prerequisites (path A)

| Need | Notes |
| --- | --- |
| Container runtime + compose | `docker` / `podman` / `nerdctl` |
| Network | Pull GHCR + reach object storage from MCP containers |
| Disk | Room for staging large `.bak` / `.bacpac` |
| One readable backup | Real object in S3/Azure (or restic repo) matching your `source_id` |
| MCP host | Cursor, Claude Desktop, or any stdio MCP client |
| OpenBao token workflow | Commands in compose RUNBOOK (Python or PowerShell for JSON). Public TLS OpenBao: [`examples/openbao-traefik/`](../examples/openbao-traefik/) |

Optional: Go toolchain (local `reksicherung` CLI), `bao` CLI on the host,
Airon + LLM only for path B.

## Architecture (operator view)

```text
You / MCP host
    │  stdio
    ▼
MCP images (gobackup | restic | sqlserver-restore | sqlserver-inspect | mysql-restore | mysql-inspect)
    │  OpenBao token only in env
    ▼
OpenBao KV  ──secrets──►  object storage + SQL connection
    │
SQL Server / MySQL (isolated restore targets)  ◄── shared volumes: artifacts (+ sql-backup for SQL Server)
cleanup-reconciler (optional but recommended)
```

Reksicherung never creates or removes those containers from restore-check skills.

## Path A — first hour

### 1. Bring up OpenBao + SQL

Follow skill [`reksicherung-bootstrap`](../.agents/skills/reksicherung-bootstrap/SKILL.md)
and **all** steps in [`examples/compose/RUNBOOK.md`](../examples/compose/RUNBOOK.md)
(pull GHCR → OpenBao → SQL → reconciler → seed → `chmod 1777`). Do not build images.
For MySQL sources also follow RUNBOOK §6b (opt-in compose profile `mysql`:
`--profile mysql up -d mysql` + `secret/reksicherung/mysql`).

### 2. Register one source

1. Copy [`examples/catalog.example.yaml`](../examples/catalog.example.yaml) to a
   working catalog; keep **one** entry that matches your real backup.
2. Restic sources **must** set `backupPath` (absolute path inside the snapshot).
3. Write OpenBao `secret/reksicherung/sources/<id>` (+ optional defaults) —
   [`schemas/openbao-source-kv.md`](schemas/openbao-source-kv.md).
4. Never put storage keys in compose `.env`.

```bash
go run ./cmd/reksicherung validate -config path/to/catalog.yaml
go run ./cmd/reksicherung catalog -config path/to/catalog.yaml > inventory.json
```

Or use the GHCR `cli` image (see RUNBOOK §9).

Skill form of this step: [`reksicherung-add-source`](../.agents/skills/reksicherung-add-source/SKILL.md).

### 3. Attach MCP servers

Configure your host with [`mcp-clients.md`](mcp-clients.md). Confirm tools list
(non-empty) before continuing.

### 4. Run one restore-check

Use skill [`reksicherung-restore-check`](../.agents/skills/reksicherung-restore-check/SKILL.md)
or the tool sequence in mcp-clients.

Minimal prompt slots ([`schemas/restore-check-prompt.md`](schemas/restore-check-prompt.md)):

```text
source_id=<your-id>
provider=gobackup|restic
backupPath=<required for restic>
cleanup=hold_for_review
```

Lab tip: `hold_for_review` keeps the DB so you can inspect it; drop later with
`reksicherung cleanup` ([`reksicherung-operate`](../.agents/skills/reksicherung-operate/SKILL.md)).

### 5. Done when

- [ ] OpenBao unsealed; KV has `sqlserver` (or `mysql` for MySQL sources) + your `sources/<id>`
- [ ] SQL Server up (and `mysql` profile service for MySQL sources); sql-backup volume mode allows uid `65532` writes
- [ ] `validate` + `catalog` succeed
- [ ] MCP tools reachable
- [ ] One run reaches restore (or fails with a **real** MCP/storage error — not a guessed PASS)
- [ ] Integrity `command_completed` is treated as "check ran", not "clean"
- [ ] No secrets in catalog or committed `.env`

### 6. Only after a good dry-run

Recurring coverage: [`reksicherung-schedule-verify`](../.agents/skills/reksicherung-schedule-verify/SKILL.md).  
Day-2 ops: [`operations.md`](operations.md).

## Optional: generate backup YAML

Only if you need new GoBackup/resticprofile configs:

1. Copy a template from [`examples/configgen/`](../examples/configgen/) (see its README for which file)
2. Skill [`reksicherung-configgen`](../.agents/skills/reksicherung-configgen/SKILL.md) — render with `reksicherung configgen`
3. Deploy generated files to the backup host (operator-owned)
4. Continue at “Register one source”

## Optional: synthetic / CI gate

E2E tests need **your** fixture paths and a disposable SQL instance —
[`test/e2e/README.md`](../test/e2e/README.md). There is no public sample `.bak`
in this repo; lab first-run uses a backup you already control in object storage.

## What not to open first

| Doc | Audience |
| --- | --- |
| [`SPEC.md`](../SPEC.md), [`docs/decisions/`](decisions/) | Product/design |
| [`docs/superpowers/`](superpowers/) | Internal planning — not operator onboarding |
| [`examples/airon/restore-check.yaml`](../examples/airon/restore-check.yaml) | Host-binary stub; not the GHCR lab path |

## Next links

| Need | Link |
| --- | --- |
| Terms | [`glossary.md`](glossary.md) |
| Compose commands | [`examples/compose/RUNBOOK.md`](../examples/compose/RUNBOOK.md) |
| MCP without Airon | [`mcp-clients.md`](mcp-clients.md) |
| Production checklist | [`operations.md`](operations.md) |
| Evidence | [`evidence.md`](evidence.md) |
| Airon lab | [`reksicherung-airon-onboard`](../.agents/skills/reksicherung-airon-onboard/SKILL.md) |
