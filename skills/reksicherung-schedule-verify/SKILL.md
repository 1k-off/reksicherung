---
name: reksicherung-schedule-verify
description: >-
  Set up recurring Reksicherung restoreability verification (inventory, batch
  selection, coverage policy, orchestrator or cron job). Use after a dry-run
  PASS when enabling regular backup checks. Airon is optional.
---

# Reksicherung schedule-verify

Turn a working one-shot restore-check into a **repeatable** verification loop.
Do not start/stop containers from this skill.

## Prerequisites

- Stack up via `docs/getting-started.md` (compose RUNBOOK), `reksicherung-bootstrap`,
  or `reksicherung-airon-onboard`.
- At least one source with a prior dry-run via `reksicherung-restore-check`.
- MCP tools reachable from the job runner (`docs/mcp-clients.md` if no Airon).
- Configgen is **not** required if backups already exist in object storage.

## Inputs

- `I-CATALOG` + refreshed `I-INVENTORY` (`reksicherung catalog -config …`)
- Coverage policy (create if missing) — see template ideas in
  `skills/reksicherung-airon-onboard/templates/coverage/POLICY.md`
- Optional `I-ORCHESTRATOR-JOB` (`docs/schemas/orchestrator-job.schema.json`)
- Optional Airon binding: `examples/airon/` or onboard pack `job/`

## Steps

1. **Inventory** — regenerate after every catalog/OpenBao source change:

```bash
reksicherung validate -config <catalog.yaml>
reksicherung catalog -config <catalog.yaml> > inventory.json
```

2. **Coverage policy** — write operator-owned policy (paths may vary):

   - `monthly_min` — minimum distinct sources checked per calendar month
   - `daily_batch` — sources per scheduled run
   - Selection: never-checked first, then oldest `last_checked_at`
   - `cleanup`: prefer `delete` for unattended; `hold_for_review` only for lab

3. **Registry journal** — maintain a local JSON/YAML journal of last check per
   `source_id` (status, `run_id`, timestamp, optional evidence path). Do not
   store secrets.

4. **Job definition** — pick one runner:

   | Runner | Action |
   | --- | --- |
   | Any MCP client + cron | Cron invokes agent/prompt that runs `reksicherung-restore-check` for the selected batch |
   | Airon | Use/adapt `airon-catalog-sweep` style job from airon-onboard templates |
   | Manual | Operator runs restore-check skill for the batch |

   Job must pass `source_id` from inventory; never embed storage credentials.

5. **Schedule** — enable only after documenting:
   - cadence (e.g. daily)
   - batch size vs `monthly_min`
   - where evidence lives (`I-EVIDENCE` / run notes)
   - who triages FAIL (`reksicherung-operate`)

6. **Month-end** — count distinct checked sources; if below `monthly_min`,
   increase `daily_batch` or run a catch-up sweep. FAIL/INCONCLUSIVE still count
   as “checked” for coverage; track status separately for quality.

## Fail closed

- No schedule until dry-run PASS on a representative source.
- Missing OpenBao / MCP → stop; do not mark coverage PASS.
- Ambiguous GoBackup archives → follow restore-check rules (no invented
  `member_path`).
- Do not require Airon; do not invent undocumented env vars.

## Related

- Per-source check: `reksicherung-restore-check`
- Triage / hold cleanup: `reksicherung-operate`
- Index: `reksicherung-index`
