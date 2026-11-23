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
- **Operator examples:** [`examples/configgen/`](../../../examples/configgen/) (MSSQL S3/Azure, SFTP, enhanced, resticprofile + Azure SAS)
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
# Requires REKSICHERUNG_OPENBAO_ADDR + REKSICHERUNG_OPENBAO_TOKEN (unsafe render).
# Writes path/to/gobackup.safe.yml + path/to/gobackup.unsafe.yml
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

3. Review outputs:
   - `*.safe.yml` — `$REKSICHERUNG_*` placeholders only
   - `*.unsafe.yml` — OpenBao-resolved secrets (mode `0600`; never commit)
4. Prefer deploying **safe** YAML. Wire `bindings.json` → OpenBao **at runtime**
   (token or AppRole → `kv get`). Use unsafe only for local/operator break-glass.
   Prefer `reksicherung openbao approle ensure -source-id <id>` then copy creds.
5. Ensure OpenBao already holds every referenced key (unsafe render fails closed).
6. Optional `hooks.run_before` / `run_after` for host dump/cleanup (Windows
   drive-letter paths OK in `archive.includes`). resticprofile Windows:
   `<project>-backup.ps1` / `<project>-cleanup.ps1` (+ `.unsafe.ps1`). Embed
   `openbao://…#key` in hook strings for secrets. Deploy YAML + sidecars.
   resticprofile still rejects `databases[]` — dump is operator-owned via hooks.
7. Continue with `reksicherung-add-source` so restore-check can discover the
   same storage via `source_id`.

## Fail closed

- Plaintext credential fields in input → Decode must reject.
- Wrong Azure auth for provider (e.g. SAS for gobackup generate) → stop.
- Do not invent OpenBao paths; refs must match real KV keys.
- Do not put resolved secrets (`*.unsafe.yml`) into git or skill transcripts.

## Next

- Register catalog + OpenBao: `reksicherung-add-source`
- Journey map: `reksicherung-index`
