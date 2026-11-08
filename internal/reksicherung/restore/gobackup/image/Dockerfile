# syntax=docker/dockerfile:1.7

# The amd64 manifest digest for mssql/server:2022-latest, resolved on
# 2026-11-08. Update the tag and digest together when intentionally refreshing
# the SQL Server base image.
FROM --platform=linux/amd64 mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718 AS sqlpackage-unpack

USER root

# Keep the extraction utility in this throwaway build stage; it is not added to
# the final restore image.
RUN apt-get update \
    && apt-get install --yes --no-install-recommends unzip=6.0-26ubuntu3.2 \
    && rm -rf /var/lib/apt/lists/*

ADD --checksum=sha256:c1b8936e5d4bf2d461f7045cf01c50cab5edfb463959af7b6f6aa8bfa45ed67b \
    https://download.microsoft.com/download/8083569f-c5e0-4547-b8dd-2c2829d9c100/sqlpackage-linux-x64-en-170.2.70.1.zip \
    /tmp/sqlpackage.zip

RUN mkdir -p /opt/sqlpackage \
    && unzip -q /tmp/sqlpackage.zip -d /opt/sqlpackage \
    && chmod 0755 /opt/sqlpackage/sqlpackage \
    && rm /tmp/sqlpackage.zip

FROM --platform=linux/amd64 mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718

COPY --from=sqlpackage-unpack /opt/sqlpackage /opt/sqlpackage

# Fail the image build if the runtime contract regresses. This checks the
# SqlPackage executable/version and the tools expected from the SQL Server base
# image; it does not perform a BACPAC import.
RUN set -eu; \
    test -x /opt/sqlpackage/sqlpackage; \
    version="$(/opt/sqlpackage/sqlpackage /Version)"; \
    printf '%s\n' "$version"; \
    printf '%s\n' "$version" | grep -F '170.2.70.1' >/dev/null; \
    test -x /usr/bin/timeout; \
    /usr/bin/timeout --version | grep -F 'GNU coreutils' >/dev/null; \
    test -x /opt/mssql-tools18/bin/sqlcmd; \
    test -x /opt/mssql/bin/sqlservr; \
    for utility in sh mkdir tee mv rm; do command -v "$utility" >/dev/null; done
