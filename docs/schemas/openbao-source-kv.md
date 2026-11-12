# OpenBao per-source KV convention

Project boundary: **one OpenBao** (or dedicated mount/namespace) per restore-check project.

## Paths

| Path | Schema |
| --- | --- |
| `secret/reksicherung/defaults` | `openbao-defaults.schema.json` |
| `secret/reksicherung/sources/<source_id>` | `openbao-source.schema.json` |
| `secret/reksicherung/sqlserver` | `openbao-sqlserver.schema.json` |

`<source_id>` equals `BackupCatalog.backups[].id`.

## Field merge (sources)

1. Load defaults (if present).
2. Load source path; source fields override defaults.
3. Fail closed if after merge required fields for the provider are empty.

## Example

```bash
bao kv put secret/reksicherung/defaults \
  S3_ENDPOINT=https://fsn1.your-objectstorage.com \
  S3_REGION=fsn1 \
  S3_BUCKET=onsite-backups-general

bao kv put secret/reksicherung/sources/shop-production \
  S3_ACCESS_KEY_ID=... \
  S3_SECRET_ACCESS_KEY=... \
  S3_PATH_PREFIX=shop/production/

bao kv put secret/reksicherung/sqlserver \
  HOST=sqlserver \
  PORT=1433 \
  USERNAME=reksicherung_restore \
  PASSWORD='...'
```

SQL Server fields (`HOST`, `USERNAME`, `PASSWORD`; `PORT` optional, default `1433`) are shared by restore, inspect, and cleanup MCPs via `secrets.ResolveSQLServer`.

Runtime refs for sources are assembled automatically, e.g.
`openbao://secret/reksicherung/sources/shop-production#S3_ACCESS_KEY_ID`.
