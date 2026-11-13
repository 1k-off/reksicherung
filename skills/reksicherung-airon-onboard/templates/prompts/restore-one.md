# Operator input: single-DB restore-check

## Restic

```
source_id=acme-prod-appdb-daily
provider=restic
database.name=AppDb
backupPath=/data/restic/AppDb-daily.bacpac
cleanup=hold_for_review
```

## GoBackup

```
source_id=contoso-web-sql-default
provider=gobackup
database.name=WebSql
cleanup=hold_for_review
archive_member=prefer path containing WebSql or *.bak
```

Procedure: `job/skills/reksicherung-restore-one/SKILL.md` (injected via
`{{file:}}` in `airon.yaml` — not as a callable skill tool).

Final line must include:
`database_name=<catalog name> restored_database=<reksicherung_…|none> run_id=<id>`

Airon tool log: `logs/run-*.json`. Step text: `outputs/restore_check.md`.
