# Reksicherung restore-check skill

Run a backup restoreability check through Reksicherung MCP tools. Do not create,
start, or stop containers. Operate only on the operator-provisioned SQL Server
and MCP services. Any orchestrator (or a human operator) may drive this skill.

Inputs: `docs/inputs.md` (`I-INVENTORY`, `I-PROMPT`). Schemas under `docs/schemas/`.

## Inputs

- Catalog inventory JSON (`reksicherung catalog`) or an explicit list of
  `source_id` values from `BackupCatalog`
- Cleanup mode: `delete` (default) or `hold_for_review`
- Optional `external_run_id` for correlation (tool arg or MCP `_meta`
  `1node.xyz/external-run-id`)
- Optional `archive_member` / disambiguation hints in the prompt (see
  `docs/schemas/restore-check-prompt.md`)

## Tool sequence (per source_id)

1. **Discover** — GoBackup or Restic list/select tools with required `source_id`.
2. **Stage** — provider `*_stage_*` tool with the same `source_id`. Keep
   `artifact_id` and `kind` (`bak` or `bacpac`).
   - **GoBackup ambiguity**: if `gobackup_stage_artifact` returns
     `code=ambiguous_archive` (or call `gobackup_list_archive_candidates`),
     choose exactly one `member_path` using `database.name` plus any prompt
     `archive_member` hints. Retry `gobackup_stage_artifact` with that
     `member_path`. If more than one candidate still matches equally, or no
     candidate clearly matches — **do not invent** — verdict `FAIL` or
     `INCONCLUSIVE`.
3. **Restore** — `sqlserver_restore_backup` with `run_id`, `artifact_id`,
   and `source_database`.
4. **Integrity** — `sqlserver_check_restore_integrity`.
5. **Semantic review** — read-only SQL MCP tools when inventory enables
   `semanticReview`. Pass the same `run_id` used for restore so inspect targets
   the run-scoped DB (`sqlserver_list_tables` / `describe` / `validate` with
   `run_id`). Do not assume `REKSICHERUNG_SQLSERVER_DATABASE=master` is the
   restore DB. If inspect cannot see the restore DB after passing `run_id` —
   verdict `INCONCLUSIVE` (not PASS).
6. **Verdict** — `PASS` / `FAIL` / `INCONCLUSIVE`; never invent PASS.
7. **Cleanup** — drop if `delete`; otherwise report held DB name. Complete
   semantic review before drop when `cleanup=delete`.

## Hard rules

- Always pass `source_id` from inventory; never invent storage credentials.
- Never request unrestricted shell, Docker/Podman, or arbitrary admin SQL.
- Never run `docker` / `podman` / `compose` for triage when MCP tools fail —
  report the MCP error and stop; the operator owns the stack.
- Never echo secret values into evidence summaries.
- If a required step fails or is skipped, final verdict is `FAIL` or
  `INCONCLUSIVE`.
- Archive member selection is fail-closed: ambiguous AI judgment is not PASS.
