# GoBackup restore helpers

This package streams one BACPAC (or nested archive member) from a GoBackup
archive for staging into an operator-managed shared volume. Import into SQL
Server is performed by `restore/sqlserver.RemoteOperations.ImportBACPAC` via a
local `sqlpackage` executable inside the restore MCP service — not by creating
containers.

## Supported archives

- Plain tar and gzip-compressed tar (`.tar.gz` / `.tgz`)
- Exactly one `.bacpac` member; path traversal, duplicates, and unsupported
  member types fail closed
- Encrypted and split archives are unsupported

`artifact.Resolve` is the preferred entry for MCP staging when the object may
already be a plain `.bak`/`.bacpac` or a nested archive.
