---
name: reksicherung-configgen
description: >-
  Author I-CONFIGGEN YAML and render GoBackup or resticprofile configs with
  secret bindings. Use when creating or updating backup-as-code for S3/MinIO/
  Azure (including SAS) before registering a catalog source.
---

# Reksicherung configgen

Produce provider YAML from the shared backup-as-code contract. Does **not**
install agents on servers, resolve OpenBao secrets, or start containers.

**Skip this skill** when backups already exist — go straight to
`reksicherung-add-source` / `docs/getting-started.md`.

## Inputs

- `I-CONFIGGEN` — `docs/schemas/configgen-input.schema.json`
- **Operator examples:** [`examples/configgen/`](../../examples/configgen/) (MSSQL S3/Azure, SFTP, enhanced, resticprofile + Azure SAS)
- Schema fixtures: `docs/schemas/fixtures/valid/configgen.*.yaml`
- Package docs: `internal/reksicherung/configgen/{input,gobackup,resticprofile}/README.md`

## Choose provider

| Target host backup tool | `-provider` | Azure auth in input |
| --- | --- | --- |
| GoBackup | `gobackup` | service principal refs only |
| restic + resticprofile | `resticprofile` | exactly one of `account_key_ref` or `account_sas_ref` |

S3/MinIO work for both. SFTP: gobackup only (resticprofile rejects).

## Steps

1. Draft YAML against `I-CONFIGGEN` (`additionalProperties: false`).
   - Credentials only as `*_ref` (`openbao://…#key`).
   - For Azure restic isolation without N app registrations: prefer
     `account_sas_ref` + per-container SAS in OpenBao.
2. Validate + render:

```bash
# Start from a template under examples/configgen/
reksicherung configgen \
  -provider gobackup \
  -in examples/configgen/gobackup-mssql-s3.yaml \
  -out path/to/gobackup.yml \
  -bindings path/to/bindings.json

reksicherung configgen \
  -provider resticprofile \
  -in examples/configgen/resticprofile-sqlbak-s3.yaml \
  -out path/to/restic.yml \
  -bindings path/to/bindings.json
```

3. Review output: placeholders `$REKSICHERUNG_*` only — no secret values.
4. Deploy generated YAML (and any companion process config) onto the backup
   host with an operator-owned method (ssh, config mgmt). Wire env from
   `bindings.json` → OpenBao (or equivalent) at runtime.
5. Ensure OpenBao already holds (or will hold) every referenced key.
6. Continue with `reksicherung-add-source` so restore-check can discover the
   same storage via `source_id`.

## Fail closed

- Plaintext credential fields in input → Decode must reject.
- Wrong Azure auth for provider (e.g. SAS for gobackup generate) → stop.
- Do not invent OpenBao paths; refs must match real KV keys.
- Do not put resolved secrets into git or skill transcripts.

## Next

- Register catalog + OpenBao: `reksicherung-add-source`
- Journey map: `reksicherung-index`
