# Prompt: enable recurring restore verification

Follow `skills/reksicherung-schedule-verify/SKILL.md` and `docs/inputs.md`.

Slots:

- `catalog_path`: BackupCatalog YAML
- `inventory_path`: where to write inventory JSON
- `coverage_policy_path`: coverage POLICY.md / equivalent
- `registry_path`: coverage journal
- `runner`: `cron+mcp` \| `airon` \| `manual`
- `daily_batch` / `monthly_min`: integers

Refresh inventory with `reksicherung validate` + `reksicherung catalog` before
enabling any schedule. Do not enable schedules without a prior dry-run PASS
(`reksicherung-restore-check`). Airon is optional.
