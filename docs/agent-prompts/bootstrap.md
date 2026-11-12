# Prompt: bootstrap Reksicherung lab

Follow `skills/reksicherung-bootstrap/SKILL.md` and `docs/inputs.md`.

Slots:

- `compose_dir`: path to `examples/compose`
- `openbao_addr`: loopback URL for host tools (e.g. `http://127.0.0.1:8200`)
- `catalog_path`: where to write `I-CATALOG`

Do not put per-backup S3 credentials in `.env`. Use OpenBao conventions in
`docs/schemas/openbao-source-kv.md`.
