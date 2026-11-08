# SQL Server restore MCP

`reksicherung-mcp-sqlserver-restore` runs bounded SQL Server restore commands
against an **already running, operator-managed SQL Server**. It never starts,
stops, or removes containers, Compose projects, or databases services. The SQL
Server, provider MCP servers, networking, and mounted volumes belong to the
operator's Compose configuration.

## Tools

- `sqlserver_restore_backup` accepts an opaque artifact ID from
  `gobackup_stage_artifact`, confirms the `.bak` header matches the requested
  source database, and restores it to a name derived from the Airon run ID.
- `sqlserver_check_restore_integrity` runs fixed, non-repair `DBCC CHECKDB`.
  `command_completed: true` means only that sqlcmd completed without an error;
  it is **not** a clean-integrity verdict.
- `sqlserver_drop_restore_database` drops only the run-derived database. It
  does not accept a database name.

There is no arbitrary SQL tool. Restore and cleanup credentials should be
limited to a disposable SQL Server instance/network dedicated to restore
checks. Native restore requires appropriate SQL Server permissions, commonly
`dbcreator` or equivalent, and CHECKDB/cleanup permissions. Do not give this
identity access to production databases.

## Shared-volume contract

The provider MCP server stages a resolved artifact as an opaque ID in
`REKSICHERUNG_ARTIFACT_STAGE_DIR`. This directory must be pre-created and
mounted at the same path in the GoBackup MCP and restore MCP containers. The
staged source artifact is retained; the restore MCP does not delete it.

`REKSICHERUNG_SQLSERVER_BACKUP_DIR` must also be pre-created and mounted at the
same absolute path in the restore MCP and SQL Server service. The restore MCP
copies the staged `.bak` into this directory, validates its header and file
list, then removes its temporary SQL backup file after the operation. Files
are created with owner/group read permissions. Configure the mount's owner,
group, and directory permissions so both processes can read/write without
changing permissions at runtime. `REKSICHERUNG_SQLSERVER_DATA_DIR` is the
absolute data directory as seen **inside SQL Server**; it need not be mounted
into the MCP process.

The GoBackup stage tool recognizes plain backup files and arbitrarily nested,
bounded ZIP, tar, or gzip-tar wrappers. It does not extract member paths to
disk; it resolves exactly one `.bak` or BACPAC database artifact. This SQL
Server MCP currently restores native `.bak` artifacts only. BACPAC imports
are rejected rather than misreported as native restores.

## Configuration

Required environment:

- `REKSICHERUNG_SQLSERVER_ADDRESS`: sqlcmd address such as `tcp:sqlserver,1433`.
- `REKSICHERUNG_SQLSERVER_RESTORE_USERNAME`: dedicated restore login.
- `REKSICHERUNG_SQLSERVER_RESTORE_PASSWORD_REF`: OpenBao reference to the
  login password.
- `REKSICHERUNG_OPENBAO_ADDR` and `REKSICHERUNG_OPENBAO_TOKEN`.
- `REKSICHERUNG_ARTIFACT_STAGE_DIR`: shared provider/restore staging mount.
- `REKSICHERUNG_SQLSERVER_BACKUP_DIR`: shared restore/SQL Server backup mount.
- `REKSICHERUNG_SQLSERVER_DATA_DIR`: SQL Server's data directory.

Optional:

- `REKSICHERUNG_SQLCMD`: sqlcmd executable, default `sqlcmd`.
- `REKSICHERUNG_ARTIFACT_MAX_BYTES`: maximum staged/native backup size, default
  200 GiB and hard-capped at 1 TiB. Set a smaller value for test environments
  with a known disk budget.

The SQL Server connection uses sqlcmd's mandatory TLS mode and certificate
verification; no trust-server-certificate override is enabled. Configure a
certificate whose name matches the address and ensure the container trusts its
issuer.

## Example Compose volumes

This fragment illustrates the shared mounts only; supply images, secrets,
networks, and service hardening in the operator's Compose file:

```yaml
services:
  gobackup-mcp:
    volumes:
      - restore-artifacts:/var/lib/reksicherung/artifacts
  sqlserver-restore-mcp:
    volumes:
      - restore-artifacts:/var/lib/reksicherung/artifacts
      - sql-backups:/var/opt/mssql/backup
  sqlserver:
    volumes:
      - sql-backups:/var/opt/mssql/backup
      - sql-data:/var/opt/mssql/data

volumes:
  restore-artifacts:
  sql-backups:
  sql-data:
```

`REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts`,
`REKSICHERUNG_SQLSERVER_BACKUP_DIR=/var/opt/mssql/backup`, and
`REKSICHERUNG_SQLSERVER_DATA_DIR=/var/opt/mssql/data` must match those paths.
Provision the volumes and ownership before starting MCP. No Compose or
container-engine command is invoked by this product.

## Artifact retention and safety

Staged source artifacts are intentionally retained for review and are never
automatically removed. Protect the volume with appropriate access controls,
monitor its free space, and remove artifacts only through an explicit operator
retention policy after verification. The S3/Restic source backup is read only:
these MCP tools never delete or mutate source backup objects.

Restore commands are bounded, use argv (not a shell), keep passwords out of
argv and protocol responses, cap sqlcmd output, and sanitize command errors.
They do not provide a clean-integrity result. No live SQL Server integration
test is implied by the unit tests.
