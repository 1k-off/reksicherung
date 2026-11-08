# GoBackup SQL Server restore adapter

GoBackup SQL Server archives contain a BACPAC export, not a native SQL Server
`.bak`. `Importer` streams the one validated BACPAC member from the tar archive
to `/var/opt/mssql/backup/reksicherung-import.bacpac` in the owned restore
container, then invokes SqlPackage's `Import` action against `localhost`.

## Required restore image

The adapter does not make the stock
`mcr.microsoft.com/mssql/server:2022-latest` image import-capable by itself.
That image must be replaced or extended with a compatible Linux SqlPackage
installation at the fixed path `/opt/sqlpackage/sqlpackage`, its required
runtime dependencies, `/bin/sh`, GNU `timeout` at `/usr/bin/timeout`, and the
`mkdir`, `tee`, `mv`, and `rm` utilities used by the container runtime. The SQL
Server process must be configured to listen on localhost port 1433, and the
supplied temporary SQL login must be authorized to create/import the target
database. Pin and validate a SqlPackage release compatible with SQL Server
2022 when building that image.

Until an image satisfying and testing these requirements is provided, this is
an adapter contract and unit-tested invocation path, not a verified
end-to-end integration with the stock SQL Server image.

## Secret and process visibility

Database name, username, and password are passed to `ExecWithEnvironment`, not
as host-side Podman argv or logged command output. The command is a fixed
`sh -c` script; caller values are passed only as environment variables and
expanded inside double quotes. No caller-controlled string is interpolated
into the script. Tool output and errors are discarded/sanitized.

SqlPackage's supported target-password input is a command-line option, so the
fixed script expands the password into SqlPackage's argv inside the container.
The password can therefore be observed by processes with sufficient access to
that container's process metadata while the import runs. This relies on the
restore container being isolated, per-run, not shared with untrusted
co-tenants, and removed after use. A stronger threat model requires a SqlPackage
credential mechanism that does not expose a secret in its process arguments.
