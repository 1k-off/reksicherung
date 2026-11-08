# GoBackup archive discovery MCP server

`reksicherung-mcp-gobackup` is a standalone MCP stdio server for bounded,
read-only GoBackup archive discovery. It complements
`internal/reksicherung/mcp/gobackup.Service`. It can stage a selected object to
a shared operator-managed volume after resolving its backup wrapper; archive
bytes and member paths never pass through MCP responses. Staging does not
modify or delete the source object.

The tools are:

- `gobackup_list`: newest-first object metadata, up to 8 records per call. The
  `limit` argument is optional and must be between 1 and 8. `truncated` is true
  when more records exist than were returned.
- `gobackup_latest`: metadata for the newest object under the configured prefix.
- `gobackup_select`: metadata for one exact, prefix-relative `id`.
- `gobackup_stage_artifact`: read one selected object, resolve one plain or
  nested `.bak`/BACPAC through bounded ZIP, tar, and gzip-tar wrappers, and
  save it under an opaque stage ID. The response contains only that ID, kind,
  and size. The SQL Server restore MCP consumes `.bak` and `.bacpac` artifacts.

The service's listing is independently capped at 1,000 objects, 10 pages, and
100 entries per page. When those bounds prevent a complete listing, the tool
returns an error rather than presenting a partial inventory as complete.
Directory markers are excluded. IDs identify exact objects and are not an
interpretation of GoBackup filename timestamps or proof that multipart backup
parts are independently restorable.

## Configuration

All settings are required. The process fails closed during startup if any are
missing, malformed, or secrets cannot be resolved. Credentials are references,
not literal S3 keys:

| Environment variable | Meaning |
| --- | --- |
| `REKSICHERUNG_GOBACKUP_S3_ENDPOINT` | S3-compatible endpoint URL. Use HTTPS except for a loopback endpoint. |
| `REKSICHERUNG_GOBACKUP_S3_REGION` | Signing region. |
| `REKSICHERUNG_GOBACKUP_S3_BUCKET` | Bucket to inspect. |
| `REKSICHERUNG_GOBACKUP_S3_PREFIX` | Required safe relative object prefix ending in `/`; scopes every operation. |
| `REKSICHERUNG_GOBACKUP_S3_ACCESS_KEY_REF` | Secret reference for the S3 access key, e.g. `openbao://secret/reksicherung/object-store#access-key`. |
| `REKSICHERUNG_GOBACKUP_S3_SECRET_KEY_REF` | Secret reference for the S3 secret key, e.g. `openbao://secret/reksicherung/object-store#secret-key`. |
| `REKSICHERUNG_OPENBAO_ADDR` | OpenBao base URL. HTTPS is required except on loopback. |
| `REKSICHERUNG_OPENBAO_TOKEN` | OpenBao token with read-only access to both referenced KV v2 values. Supply through the MCP host's protected environment mechanism. |
| `REKSICHERUNG_ARTIFACT_STAGE_DIR` | Pre-created volume shared with the SQL Server restore MCP; staged artifacts remain until operator retention cleanup. |
| `REKSICHERUNG_ARTIFACT_MAX_BYTES` | Maximum source object and final artifact size. Defaults to 200 GiB; hard maximum is 1 TiB. |

The command resolves references through the provider-neutral
`secrets.SecretProvider` interface using the existing OpenBao KV v2 provider.
The OpenBao token is only read from the process environment. S3 credentials and
the token are never command-line arguments or logged. Startup diagnostics go to
stderr; stdout is reserved for MCP protocol messages. MCP input frames are
limited to 64 KiB. The staging directory is never created or chmod'ed by the
process; mount and provision it before startup with permissions suitable for
the provider and restore MCP service identities. Staged artifacts are retained
for explicit operator review and are not automatically removed.

## Build

```sh
go build -o ./bin/reksicherung-mcp-gobackup ./cmd/reksicherung-mcp-gobackup
```

## Airon MCP configuration

Airon expands `${NAME}` placeholders before starting MCP children. Supply the
OpenBao token in Airon's protected environment and set `env_passthrough: false`
so the child receives only the explicitly configured environment. Do not place
literal credentials in arguments or checked-in configuration.

```yaml
runtime:
  log_inputs: false

mcp_servers:
  - name: reksicherung_gobackup
    transport: stdio
    command: /usr/local/bin/reksicherung-mcp-gobackup
    env_passthrough: false
    env:
      REKSICHERUNG_GOBACKUP_S3_ENDPOINT: ${REKSICHERUNG_GOBACKUP_S3_ENDPOINT}
      REKSICHERUNG_GOBACKUP_S3_REGION: ${REKSICHERUNG_GOBACKUP_S3_REGION}
      REKSICHERUNG_GOBACKUP_S3_BUCKET: ${REKSICHERUNG_GOBACKUP_S3_BUCKET}
      REKSICHERUNG_GOBACKUP_S3_PREFIX: ${REKSICHERUNG_GOBACKUP_S3_PREFIX}
      REKSICHERUNG_GOBACKUP_S3_ACCESS_KEY_REF: ${REKSICHERUNG_GOBACKUP_S3_ACCESS_KEY_REF}
      REKSICHERUNG_GOBACKUP_S3_SECRET_KEY_REF: ${REKSICHERUNG_GOBACKUP_S3_SECRET_KEY_REF}
      REKSICHERUNG_OPENBAO_ADDR: ${REKSICHERUNG_OPENBAO_ADDR}
      REKSICHERUNG_OPENBAO_TOKEN: ${REKSICHERUNG_OPENBAO_TOKEN}
      REKSICHERUNG_ARTIFACT_STAGE_DIR: /var/lib/reksicherung/artifacts
      REKSICHERUNG_ARTIFACT_MAX_BYTES: ${REKSICHERUNG_ARTIFACT_MAX_BYTES}
    volumes:
      - restore-artifacts:/var/lib/reksicherung/artifacts
```

Airon's tool names for this server are
`reksicherung_gobackup__gobackup_list`,
`reksicherung_gobackup__gobackup_latest`, and
`reksicherung_gobackup__gobackup_select`. Keep Airon's input logging disabled
for deployments that might log environment/configuration material. Never put a
token or S3 credential literal in MCP arguments, this file, or logs.

The `restore-artifacts` volume must also be mounted at the same path in
`reksicherung-mcp-sqlserver-restore`. Do not mount it into a SQL Server data
path; the restore MCP copies `.bak` data to a separate volume shared with SQL
Server. The SQL Server MCP and Compose contract are documented in
[`../reksicherung-mcp-sqlserver-restore/README.md`](../reksicherung-mcp-sqlserver-restore/README.md).
