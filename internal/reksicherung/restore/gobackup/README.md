# GoBackup SQL Server restore adapter

GoBackup SQL Server archives contain a BACPAC export, not a native SQL Server
`.bak`. `Importer` streams the one validated BACPAC member from the tar archive
to `/var/opt/mssql/backup/reksicherung-import.bacpac` in the owned restore
container, then invokes SqlPackage's `Import` action against `localhost`. The
target database name must be derived from the managed run ID by
`restore/sqlserver.DatabaseNameForRun`; this prevents the importer and cleanup
adapter from targeting SQL Server system databases or unrelated databases.

## Required restore image

The adapter does not make the stock
`mcr.microsoft.com/mssql/server:2022-latest` image import-capable by itself.
The reproducible amd64 image definition is in
[`image/`](image/README.md). It pins a SQL Server 2022 base image and SqlPackage
170.2.70.1, and its build checks for SqlPackage, GNU `timeout` at
`/usr/bin/timeout`, `/opt/mssql-tools18/bin/sqlcmd`, and the shell/file
utilities required by the container runtime. Build and smoke-check instructions,
source provenance, architecture, and license assumptions are documented there.

The image checks verify executable presence and SqlPackage's reported version;
they do not exercise a BACPAC import. The SQL Server process must be configured
to listen on localhost port 1433, and the supplied temporary SQL login must be
authorized to create/import the target database. Compatibility of this
SqlPackage release with actual GoBackup BACPAC archives and SQL Server 2022 is
not claimed until a representative end-to-end import is tested.

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
