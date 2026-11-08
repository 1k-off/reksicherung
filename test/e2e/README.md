# E2E release gate

```bash
export REKSICHERUNG_E2E_SQLSERVER_ADDRESS='tcp:127.0.0.1,1433'
export REKSICHERUNG_E2E_SQLSERVER_USER='reksicherung_restore'
export REKSICHERUNG_E2E_SQLSERVER_PASSWORD='...'
export REKSICHERUNG_E2E_BACKUP_DIR='/var/opt/mssql/backup'
export REKSICHERUNG_E2E_DATA_DIR='/var/opt/mssql/data'
export REKSICHERUNG_E2E_BAK_FIXTURE='/path/to/Example.bak'

go test -tags=e2e ./test/e2e/...
```

Tests skip when required environment variables are missing. They must never
point at production databases or mutate source backup buckets.
