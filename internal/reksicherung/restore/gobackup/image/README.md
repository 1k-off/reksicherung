# GoBackup SQL Server restore image

This directory defines an amd64 restore image by extending a pinned SQL Server
2022 container image with the Linux SqlPackage executable at
`/opt/sqlpackage/sqlpackage`. It adds no credentials and takes no secrets as
build arguments.

## Build

Run from the repository root with Docker BuildKit enabled:

```sh
DOCKER_BUILDKIT=1 docker build \
  --platform linux/amd64 \
  --file internal/reksicherung/restore/gobackup/image/Dockerfile \
  --tag reksicherung-sqlserver-restore:2022 .
```

The Dockerfile pins both the SQL Server base image manifest and the SqlPackage
archive. The base image is the `linux/amd64` manifest for
`mcr.microsoft.com/mssql/server:2022-latest` resolved on 2026-11-08
(`sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718`).
Its registry metadata identifies Ubuntu 22.04 and SQL Server
`16.0.4305.5`. Refreshing SQL Server means deliberately updating the pinned
digest after checking the new base image and rerunning the smoke checks below.

The SqlPackage dependency is Microsoft SqlPackage **170.2.70.1** (release
170.2.70, dated 2025-10-14), from Microsoft's standalone Linux x64 distribution:

- [Microsoft SqlPackage download and installation](https://learn.microsoft.com/en-us/sql/tools/sqlpackage/sqlpackage-download?view=sql-server-ver17)
- [Microsoft SqlPackage release notes](https://learn.microsoft.com/en-us/sql/tools/sqlpackage/release-notes-sqlpackage?view=sql-server-ver17)
- [Pinned Microsoft download](https://download.microsoft.com/download/8083569f-c5e0-4547-b8dd-2c2829d9c100/sqlpackage-linux-x64-en-170.2.70.1.zip)

The Microsoft download page does not publish a checksum for this archive. The
Dockerfile pins the SHA-256 calculated from that downloaded archive
(`c1b8936e5d4bf2d461f7045cf01c50cab5edfb463959af7b6f6aa8bfa45ed67b`) using
BuildKit's `ADD --checksum`, so a changed or incorrectly fetched file fails the
build. The version and archive hash are explicit rather than using the
evergreen `aka.ms/sqlpackage-linux` link. SqlPackage is self-contained; the
temporary unzip helper is installed only in the build stage and is not copied
into the final image. The build-only extraction package is pinned to Ubuntu
Jammy's `unzip` version `6.0-26ubuntu3.2`.

## Architecture and license assumptions

This definition is intentionally `linux/amd64` only: the pinned SQL Server
container manifest and the SqlPackage archive are both x64. It does not claim
arm64 support.

SqlPackage's NuGet package license file identifies Microsoft Software License
Terms for SQL Server Data-Tier Application Framework and calls the software
"Distributable Code." The terms include conditions for distribution as part of
an application, including significant primary functionality and downstream
license terms; they also restrict standalone distribution/hosting. This image
is assumed to be an internal restore component of the application, not a
standalone SqlPackage offering. Review the applicable Microsoft and third-party
terms in the [Microsoft.SqlPackage 170.2.70 NuGet package](https://www.nuget.org/packages/Microsoft.SqlPackage/170.2.70)
before distributing or hosting the image; this note is not legal advice.
The SQL Server base image has separate SQL Server licensing and EULA
requirements. Accept and supply those at runtime as required by the SQL Server
image's [container deployment documentation](https://learn.microsoft.com/en-us/sql/linux/sql-server-linux-docker-container-deployment?view=sql-server-ver16);
do not put passwords or other runtime secrets in this Dockerfile or its build
arguments.

## Smoke check and compatibility scope

The Dockerfile runs these checks during build: SqlPackage is executable and
reports version `170.2.70.1`, `/usr/bin/timeout` identifies itself as GNU
coreutils, `sqlcmd` and `sqlservr` are executable at their base-image paths,
and the shell/file utilities required by the restore adapter are present.
After building, repeat the checks against the tagged image without starting a
SQL Server process or passing credentials:

```sh
docker run --rm --platform linux/amd64 \
  --entrypoint /bin/sh reksicherung-sqlserver-restore:2022 -ec '
    test -x /opt/sqlpackage/sqlpackage
    /opt/sqlpackage/sqlpackage /Version
    /opt/sqlpackage/sqlpackage /Version | grep -F 170.2.70.1
    /usr/bin/timeout --version | grep -F "GNU coreutils"
    test -x /opt/mssql-tools18/bin/sqlcmd
    test -x /opt/mssql/bin/sqlservr
    for tool in sh mkdir tee mv rm; do command -v "$tool"; done
  '
```

These are executable-presence and version checks only. They do **not** prove
that this SqlPackage build can import every GoBackup BACPAC or that a particular
archive is compatible with SQL Server 2022. That requires an end-to-end import
with a representative, non-sensitive BACPAC fixture and an initialized server.
Until that test is run, compatibility remains unverified.
