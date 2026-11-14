# Prompt: bootstrap Reksicherung lab

Follow `skills/reksicherung-bootstrap/SKILL.md`, `docs/inputs.md`, and the
copy-paste steps in `examples/compose/RUNBOOK.md` (do not invent OpenBao/SQL
commands).

**Pull GHCR images. Do not `docker compose build`** unless the operator
explicitly asked for developer/source builds.

Slots:

- `compose_dir`: path to `examples/compose`
- `openbao_addr`: loopback URL for host tools (e.g. `http://127.0.0.1:8200`)
- `catalog_path`: where to write `I-CATALOG`
- `image_tag`: optional (`latest` default; pin for production)

Do not put per-backup S3 credentials in `.env`. Use OpenBao conventions in
`docs/schemas/openbao-source-kv.md`. Skip `reksicherung-configgen` unless the
operator needs new backup YAML.
