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
4. `STORAGE_TYPE` is required after merge (`s3` or `azure`). Cross-type fields fail closed.

## S3 example

```bash
bao kv put secret/reksicherung/defaults \
  STORAGE_TYPE=s3 \
  S3_ENDPOINT=https://objects.example.test \
  S3_REGION=eu-central-1 \
  S3_BUCKET=demo-backups

bao kv put secret/reksicherung/sources/demo-app-a \
  S3_ACCESS_KEY_ID=... \
  S3_SECRET_ACCESS_KEY=... \
  S3_PATH_PREFIX=demo-a/prod/
```

## Azure auth (exactly one mode)

| Mode | Fields | Typical use |
| --- | --- | --- |
| SAS | `AZURE_ACCOUNT_SAS` | Per-container isolation without N app registrations (restic + MCP Store) |
| Account key | `AZURE_ACCOUNT_KEY` | Whole storage account access |
| Service principal | `AZURE_TENANT_ID` + `AZURE_CLIENT_ID` + `AZURE_CLIENT_SECRET` | GoBackup configgen / Store with RBAC |

```bash
bao kv put secret/reksicherung/defaults \
  STORAGE_TYPE=azure \
  AZURE_ACCOUNT_NAME=demostorage0001

bao kv put secret/reksicherung/sources/demo-app-a \
  AZURE_CONTAINER=demoa \
  AZURE_ACCOUNT_SAS='sv=...&sig=...' \
  AZURE_PATH_PREFIX=production/
```

Restic Azure sources need `RESTIC_REPOSITORY=azure:<container>:/<prefix>`,
`RESTIC_PASSWORD`, and either `AZURE_ACCOUNT_KEY` or `AZURE_ACCOUNT_SAS`
(not SP-only). GoBackup configgen still maps Azure via service principal only.

## SQL Server

```bash
bao kv put secret/reksicherung/sqlserver \
  HOST=sqlserver \
  PORT=1433 \
  USERNAME=reksicherung_restore \
  PASSWORD='...'
```

SQL Server fields (`HOST`, `USERNAME`, `PASSWORD`; `PORT` optional, default `1433`) are shared by restore, inspect, and cleanup MCPs via `secrets.ResolveSQLServer`.

Runtime refs for sources are assembled automatically, e.g.
`openbao://secret/reksicherung/sources/demo-app-a#S3_ACCESS_KEY_ID`.
