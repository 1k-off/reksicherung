# Restic + SQL Server restore image

This amd64 image extends the pinned SQL Server 2022 base with Restic
`0.17.3`, required by the Restic-to-native-`.bak` pipeline. The Restic binary
is unpacked in a throwaway build stage and copied into the final SQL Server
image. It adds no credentials and accepts no secret build arguments.

Build from the repository root with BuildKit:

```sh
DOCKER_BUILDKIT=1 docker build \
  --platform linux/amd64 \
  --file internal/reksicherung/restore/restic/image/Dockerfile \
  --tag reksicherung-restic-sqlserver:2022 .
```

The SQL Server base is pinned to the amd64 manifest digest also used by the
GoBackup restore image:
`mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718`.
Refresh it only after reviewing the SQL Server version and rerunning image
checks.

Restic is the upstream `v0.17.3` Linux amd64 release asset:

- Release: <https://github.com/restic/restic/releases/tag/v0.17.3>
- Asset: <https://github.com/restic/restic/releases/download/v0.17.3/restic_0.17.3_linux_amd64.bz2>
- SHA-256: `5097faeda6aa13167aae6e36efdba636637f8741fed89bbf015678334632d4d3`
- Upstream checksum manifest: <https://github.com/restic/restic/releases/download/v0.17.3/SHA256SUMS>
- Upstream release verification procedure: <https://github.com/restic/restic/blob/master/helpers/verify-release-binaries.sh>

BuildKit verifies the asset hash before unpacking; the image build checks that
the installed executable reports `restic 0.17.3`, alongside SQL Server and
`sqlcmd` tool presence. The checksum is pinned from the upstream release
manifest. Operators should verify the signed upstream manifest using Restic's
published release verification procedure before intentionally refreshing this
binary version. The unpacking stage uses the `bzip2` executable already
included in the digest-pinned SQL Server base image; no package installation
or mutable apt repository is involved.

The image definition is linux/amd64 only. The Dockerfile's checks establish
binary presence and version, not that a particular Restic repository or SQL
Server backup can be restored. No live end-to-end compatibility claim is made.
