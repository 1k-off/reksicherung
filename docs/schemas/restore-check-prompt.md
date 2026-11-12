# Restore-check prompt slots

Structured slots the operator prompt (or skill input) must supply. Free-form
prose around these slots is allowed; inventing undeclared credentials is not.

| Slot | Purpose |
| --- | --- |
| `source_id` / inventory | Which catalog sources to check |
| `cleanup` | `delete` or `hold_for_review` |
| `external_run_id` | Optional orchestrator correlation ID for evidence |
| semantic-review scope | Tables/queries when inventory enables AI review |
| `archive_member` | Optional disambiguation when a GoBackup archive contains multiple `.bak`/`.bacpac` members. Free-form hints are allowed (e.g. prefer path containing `database.name`, prefer `*.bacpac`). If hints still leave multiple equally valid candidates, the run must FAIL/INCONCLUSIVE — do not invent a path. |
| semantic `run_id` | Same `run_id` as restore/integrity; pass it to inspect MCP tools so they open the run-scoped database when the process default is `master`. |

Optional MCP `_meta` key for correlation: `1node.xyz/external-run-id`.

## GoBackup member selection

1. Deterministic code prefers a unique terminal `.bak`/`.bacpac` over nested wrappers.
2. If multiple terminals remain, MCP returns `ambiguous_archive` with candidate
   paths (or use `gobackup_list_archive_candidates`).
3. The AI selects one `member_path` using catalog `database.name` and this
   prompt slot, then retries `gobackup_stage_artifact` with `member_path`.
4. Restic sources already disambiguate via catalog `backupPath`; this slot does
   not apply to them.
