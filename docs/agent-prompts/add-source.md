# Prompt: add backup source

Follow `skills/reksicherung-add-source/SKILL.md`.

Slots:

- `catalog_path`
- `source_id`
- `provider` (`gobackup` | `restic`)
- `database_name`
- OpenBao field values for `I-OPENBAO-SOURCE` (never echo secrets back in full)

Validate with `reksicherung validate` before finishing.
