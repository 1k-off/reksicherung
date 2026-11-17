# Configgen examples (`I-CONFIGGEN`)

Fictional OpenBao refs and hostnames only. Copy a file, replace refs/paths with
your project values, then render.

Schema: [`docs/schemas/configgen-input.schema.json`](../../docs/schemas/configgen-input.schema.json)  
Skill: [`.agents/skills/reksicherung-configgen/SKILL.md`](../../.agents/skills/reksicherung-configgen/SKILL.md)  
Skip this folder if backups already exist — go to
[`docs/getting-started.md`](../../docs/getting-started.md).

## Which example?

| File | Provider (`-provider`) | Storage | What it backs up |
| --- | --- | --- | --- |
| [`gobackup-mssql-s3.yaml`](gobackup-mssql-s3.yaml) | `gobackup` | S3 / MinIO | SQL Server via GoBackup mssql plugin (`.bacpac`) |
| [`gobackup-mysql-s3.yaml`](gobackup-mysql-s3.yaml) | `gobackup` | S3 / MinIO | MySQL via GoBackup mysql plugin (`port` defaults to 3306) |
| [`gobackup-mssql-azure-sp.yaml`](gobackup-mssql-azure-sp.yaml) | `gobackup` | Azure | Same, Azure **service principal** only |
| [`gobackup-archive-sftp.yaml`](gobackup-archive-sftp.yaml) | `gobackup` | SFTP | File archive (gobackup-only storage) |
| [`gobackup-enhanced-s3.yaml`](gobackup-enhanced-s3.yaml) | `gobackup` | S3 | MSSQL + daily/weekly/monthly schedules |
| [`resticprofile-sqlbak-s3.yaml`](resticprofile-sqlbak-s3.yaml) | `resticprofile` | S3 / MinIO | Native `.bak` path in restic snapshots |
| [`resticprofile-hooks-windows.yaml`](resticprofile-hooks-windows.yaml) | `resticprofile` | S3 / MinIO | Windows drive-letter path + `hooks` dump/cleanup |
| [`resticprofile-azure-sas.yaml`](resticprofile-azure-sas.yaml) | `resticprofile` | Azure | Archive + Azure **SAS** (or swap to `account_key_ref`) |

## Render

From the repository root (after `go build` / installed `reksicherung` CLI):

```bash
# Needs OpenBao env for the unsafe file:
#   REKSICHERUNG_OPENBAO_ADDR + REKSICHERUNG_OPENBAO_TOKEN
# Writes /tmp/gobackup.safe.yml + /tmp/gobackup.unsafe.yml (+ bindings)
reksicherung configgen \
  -provider gobackup \
  -in examples/configgen/gobackup-mssql-s3.yaml \
  -out /tmp/gobackup.yml \
  -bindings /tmp/bindings.json

reksicherung configgen \
  -provider resticprofile \
  -in examples/configgen/resticprofile-sqlbak-s3.yaml \
  -out /tmp/restic.yml \
  -bindings /tmp/bindings.json
```

- **`*.safe.yml`** — `$REKSICHERUNG_*` placeholders; deploy this and resolve at
  runtime (token or AppRole → `kv get`).
- **`*.unsafe.yml`** — secrets already substituted from OpenBao (`0600`);
  local/operator use only — never commit.

Optional `hooks.run_before` / `hooks.run_after` map to GoBackup
`before_script`/`after_script`, and for resticprofile to **sidecar scripts**
beside `-out` (`.ps1` when archive paths are Windows drive-letter, else `.sh`)
with profile hooks calling `powershell -File …` / `sh …` — workaround for
resticprofile [#582](https://github.com/creativeprojects/resticprofile/issues/582).
Deploy the YAML and sidecars together. Dump production for resticprofile is
operator-owned (hooks + `archive.includes`); `databases[]` stays GoBackup-only.

## After render

1. Deploy **`*.safe.yml`** (not unsafe) to the backup host; wire bindings at runtime.
2. Ensure every `openbao://…#key` in the input exists in OpenBao.
3. Register the same storage for restore-check:
   [`reksicherung-add-source`](../../.agents/skills/reksicherung-add-source/SKILL.md)
   (catalog `source_id` + `secret/reksicherung/sources/<id>` with `STORAGE_TYPE`).

For restic catalog entries, set `backupPath` to the same absolute `.bak` path
used in `archive.includes` (see `resticprofile-sqlbak-s3.yaml`).

## Provider rules (short)

| | GoBackup | resticprofile |
| --- | --- | --- |
| Databases (`mssql` / `mysql` / …) | yes | no — use hooks + `archive` of `.bak`/`.bacpac` |
| Hooks (`run_before` / `run_after`) | `before_script` / `after_script` | `backup.run-before` / `run-after` |
| Archive paths | POSIX or Windows drive-letter | POSIX or Windows drive-letter |
| SFTP | yes | no |
| Azure auth | service principal refs | exactly one of `account_key_ref` / `account_sas_ref` |
| Encryption | optional openssl | **required** `encryption.password_ref` → `RESTIC_PASSWORD` |

## Schema fixtures

Machine-checkable copies also live under
`docs/schemas/fixtures/valid/configgen.*.yaml` (keep examples and fixtures in sync
when changing the contract).
