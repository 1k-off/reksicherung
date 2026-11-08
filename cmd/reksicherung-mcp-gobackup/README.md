# GoBackup archive discovery MCP server

`reksicherung-mcp-gobackup` is a standalone MCP stdio server for bounded,
read-only GoBackup archive discovery. It complements
`internal/reksicherung/mcp/gobackup.Service`; it does not restore archives and
does not expose object bytes or an object-reading tool.

The three tools are:

- `gobackup_list`: newest-first object metadata, up to 8 records per call. The
  `limit` argument is optional and must be between 1 and 8. `truncated` is true
  when more records exist than were returned.
- `gobackup_latest`: metadata for the newest object under the configured prefix.
- `gobackup_select`: metadata for one exact, prefix-relative `id`.

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

The command resolves references through the provider-neutral
`secrets.SecretProvider` interface using the existing OpenBao KV v2 provider.
The OpenBao token is only read from the process environment. S3 credentials and
the token are never command-line arguments or logged. Startup diagnostics go to
stderr; stdout is reserved for MCP protocol messages. MCP input frames are
limited to 64 KiB.

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
```

Airon's tool names for this server are
`reksicherung_gobackup__gobackup_list`,
`reksicherung_gobackup__gobackup_latest`, and
`reksicherung_gobackup__gobackup_select`. Keep Airon's input logging disabled
for deployments that might log environment/configuration material. Never put a
token or S3 credential literal in MCP arguments, this file, or logs.
