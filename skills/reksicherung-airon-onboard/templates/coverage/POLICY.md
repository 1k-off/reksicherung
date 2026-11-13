# Coverage policy (example)

Catalog size ~100. Target: **at least 60 distinct** restore-checks per
**calendar month**.

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `monthly_min` | 60 | Contract / audit bar |
| `daily_batch` | 2 | 60 / ~30 days |
| `oldest_window` | 10 | Rank never-checked + oldest; take `daily_batch` from window |
| Selection | agent via `fs_read` on catalog + registry | never-checked → oldest `last_checked_at` |
| `cleanup` | `delete` for unattended; `hold_for_review` for lab debug | |

## Files

| Path | Role |
|------|------|
| `coverage/registry.json` | Journal; must include `database_name` ↔ `restored_database` |
| `coverage/runs/YYYY-MM-DD/<source_id>.md` | Per-check evidence (agent `fs_write`) |
| `catalog/catalog.yaml` | Ids / provider / `database.name` / backupPath |
| `logs/run-*.json` | Airon tool-call log (workspace = package root) |
| `outputs/<step>.md` | Airon step final text |

The catalog-sweep skill **selects and updates** these with built-in `fs_read` /
`fs_write`. Do not ship host pick/merge scripts for coverage.

## Required per-check fields

- `database_name` — catalog `database.name`
- `restored_database` — MCP restore `database` (`reksicherung_<32hex>`), or null
- `last_run_id` — restore `run_id`
- `last_report` — `coverage/runs/…` path

## Daily schedule (operator)

1. `airon run --config job/airon-catalog-sweep.yaml` with optional overrides
2. Agent picks batch, restore-checks, writes evidence + registry
3. Optional: publish `coverage/report-YYYY-MM.md` (counts vs `monthly_min`)

## Month-end check

```text
checked_this_month = count(registry where checked_at in month AND status in pass|fail|inconclusive)
ok if checked_this_month >= monthly_min
```

Failures still count as “checked” for coverage; track `status` for quality.

If `gobackup_latest` works but `gobackup_stage_artifact` fails, mark that source
FAIL in the registry and rotate — do not treat it as a dead support stack.
