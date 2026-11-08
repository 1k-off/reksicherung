# syntax=docker/dockerfile:1.7

FROM --platform=linux/amd64 mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718 AS restic-unpack

USER root

ADD --checksum=sha256:5097faeda6aa13167aae6e36efdba636637f8741fed89bbf015678334632d4d3 \
    https://github.com/restic/restic/releases/download/v0.17.3/restic_0.17.3_linux_amd64.bz2 \
    /tmp/restic_0.17.3_linux_amd64.bz2

RUN mkdir -p /opt/restic \
    && bzip2 -dc /tmp/restic_0.17.3_linux_amd64.bz2 > /opt/restic/restic \
    && chmod 0755 /opt/restic/restic \
    && rm /tmp/restic_0.17.3_linux_amd64.bz2

FROM --platform=linux/amd64 mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718

COPY --from=restic-unpack /opt/restic/restic /usr/local/bin/restic

RUN set -eu; \
    version="$(restic version)"; \
    printf '%s\n' "$version"; \
    printf '%s\n' "$version" | grep -F 'restic 0.17.3' >/dev/null; \
    test -x /opt/mssql-tools18/bin/sqlcmd; \
    test -x /opt/mssql/bin/sqlservr; \
    for utility in sh mkdir tee mv rm; do command -v "$utility" >/dev/null; done
