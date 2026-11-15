---
name: reksicherung-catalog-sweep
description: >-
  Procedure for coverage restore-checks. Inject into the Airon prompt via
  {{file:}}; do not list as prompts.tools (airon skill tools have no tool loop).
---

# Catalog coverage sweep (oldest / never-checked)

Goal: over a calendar month, restore-check enough distinct `source_id`s to meet
the operator **monthly_min** (example: catalog ≈100, **monthly_min=60** → about
**2/day**).

You **select** the batch and **run MCP restore tools yourself**. No host pick
script. Workspace root is the package root (`catalog/`, `coverage/` readable).

For a single ad-hoc id, use `reksicherung-restore-one` / `airon.yaml` instead.

## Selection (do this first)

1. `fs_read` `catalog/catalog.yaml` — ids / provider / `database.name` /
   restic `backupPath`.
2. `fs_read` `coverage/registry.json`. If missing, bootstrap from
   `coverage/registry.example.json` via `fs_write` (`sources: {}` is fine).
3. Optional: `fs_read` `coverage/POLICY.md`.
4. Rank every catalog `source_id`:
   - **Never-checked** (absent from `registry.sources`, or no `last_checked_at`)
     first.
   - Then ascending `last_checked_at` (oldest first).
5. Take the top `oldest_window` (default **10**), then pick **`daily_batch`**
   (default **2**) from that window (first N is fine).
6. Operator input may override `daily_batch=`, `oldest_window=`, `cleanup=`, or
   force a single `source_id=` — never invent ids.

## Per-source pipeline

Same as `reksicherung-restore-one` (discover → stage → restore → integrity →
cleanup). Short unique `run_id` per source (`rc-1`, `rc-2`, …).

1. Discover (matching provider; gobackup miss on restic is OK).
2. Stage — restic needs exact `backupPath` from the catalog row; never invent.
3. Restore with short `run_id`.
4. From restore MCP JSON, keep `database` as **`restored_database`** and
   catalog `database.name` as **`database_name`**.
5. Integrity; optional inspect with the **same** `run_id`.
6. Cleanup per `cleanup=` (default `delete`).
7. **Write evidence** then update registry (below).

Process members **sequentially**.

## Evidence log (required)

After each source, `fs_write`:

`coverage/runs/<YYYY-MM-DD>/<source_id>.md`

```markdown
# <source_id>
- checked_at: <RFC3339 UTC now>
- status: pass|fail|inconclusive
- provider: gobackup|restic
- database_name: <catalog database.name>
- restored_database: <reksicherung_… from restore MCP, or none>
- run_id: <restore run_id>
- artifact_id: <id or none>
- cleanup: delete|hold_for_review
- error: <short MCP error or empty>
```

Do not invent `restored_database` — copy the `database` field from
`restore__sqlserver_restore_backup` (or integrity/drop) only.

## Registry update (required)

After **each** source — including staging failures — `fs_write`
`coverage/registry.json`:

- `last_checked_at` — **real current UTC** RFC3339 (never invent past dates)
- `last_status` — `pass` | `fail` | `inconclusive`
- `last_run_id` — restore `run_id`, or `""`
- `provider`, `database_name`, `restored_database` (or `null`)
- `last_report` — path of the evidence file
- optional `last_error`
- preserve `version` / `policy` and other sources

## Report

Airon `pass_fail` only accepts `PASS:` / `FAIL:` — never end with
`INCONCLUSIVE:`.

1. `PASS:` only if every member restore+integrity OK; else `FAIL:` with counts.
2. Required mapping block:

```text
### restore_map
- source_id: <id> database_name: <name> restored_database: <reksicherung_…|none> status: pass|fail|inconclusive run_id: <id> report: coverage/runs/…
```

## Hard rules

- Never invent catalog ids, credentials, `backupPath`, or `restored_database`.
- Never read/write `compose/`, `*.env`, `openbao-init.json`, or seed scripts.
- No docker/podman/compose for triage — MCP only.
- Do not exceed `daily_batch` (unless a single forced `source_id=`).
- Always end with `PASS:` or `FAIL:` after evidence + registry writes.
