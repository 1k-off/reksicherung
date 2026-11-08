# Restic snapshot discovery MCP server

`reksicherung-mcp-restic` is a standalone MCP server using newline-delimited
JSON-RPC over stdio. It queries one configured Restic repository and exposes
snapshot identifiers and a small set of descriptive metadata. It does not
listen on a network port.

## Tools and limits

The server registers only:

- `restic_list_snapshots`: return a caller-bounded list of recent snapshots
  (1–100) and a truncation flag.
- `restic_select_snapshot`: validate one full snapshot ID and return its
  metadata. This is an explicit metadata selection only; it does not restore,
  extract, or read files.

The tools return only snapshot ID, time, hostname, and tags. File paths, file
names, tree IDs, file contents, and repository data are not returned. Inputs
reject unknown properties. Snapshot IDs must be full 64-character hexadecimal
IDs and cannot select another repository. The service uses argv-based
`restic snapshots --json` invocations without a shell, a 15-second operation
timeout, a 4 MiB process-output cap, and strict JSON decoding. Each incoming
stdio frame is limited to 64 KiB. Restic stderr and SDK diagnostics are
discarded; startup and process errors go to stderr, and stdout is reserved for
MCP protocol traffic.

These are read-only MCP operations; they do not issue restore or mutation
commands. Restic itself may update its local cache or briefly create repository
lock metadata while reading. Run the process with a dedicated OS account and
read-only repository credentials/permissions where the repository backend
supports them. A read-only UI does not replace backend access controls.

## Configure

Required process configuration:

- `REKSICHERUNG_RESTIC_REPOSITORY`: the single Restic repository URL/path.
- `REKSICHERUNG_RESTIC_PASSWORD_REF`: an OpenBao reference such as
  `openbao://secret/reksicherung/restic#repository-password`.
- `REKSICHERUNG_OPENBAO_ADDR`: the OpenBao base URL. HTTPS is required except
  for loopback endpoints used in local development.
- `REKSICHERUNG_OPENBAO_TOKEN`: the protected OpenBao bootstrap token. This is
  the only secret value supplied directly through the process environment.

Optional remote backend credentials are configured only as OpenBao references:

| Restic child variable | Reference environment variable |
| --- | --- |
| `AWS_ACCESS_KEY_ID` | `REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF` |
| `AWS_SECRET_ACCESS_KEY` | `REKSICHERUNG_RESTIC_AWS_SECRET_ACCESS_KEY_REF` |
| `AWS_SESSION_TOKEN` | `REKSICHERUNG_RESTIC_AWS_SESSION_TOKEN_REF` |
| `B2_ACCOUNT_ID` | `REKSICHERUNG_RESTIC_B2_ACCOUNT_ID_REF` |
| `B2_ACCOUNT_KEY` | `REKSICHERUNG_RESTIC_B2_ACCOUNT_KEY_REF` |
| `AZURE_ACCOUNT_NAME` | `REKSICHERUNG_RESTIC_AZURE_ACCOUNT_NAME_REF` |
| `AZURE_ACCOUNT_KEY` | `REKSICHERUNG_RESTIC_AZURE_ACCOUNT_KEY_REF` |

Each configured reference must use `openbao://<mount>/<path>#<key>`. AWS
`AWS_DEFAULT_REGION` and `AWS_REGION` remain optional non-secret configuration
and are passed through explicitly. The repository string, OpenBao address,
secret references, and AWS region are configuration rather than credential
values (the repository and address can still be operationally sensitive); no
other parent environment variables are inherited or forwarded. The server
rejects repository URLs with embedded password userinfo or query parameters
named like credentials, as well as password-form SFTP userinfo, while allowing
standard local, SFTP, S3, B2, and Azure repository forms.

The server resolves references once at startup through `secrets.SecretProvider`
and the OpenBao KV v2 provider. Resolved values are passed only to the Restic
child process and are never included in diagnostics. OpenBao token renewal is
not performed; provision a token valid for the process lifetime. Environment
variables can be visible to other processes running under the same OS account,
so use a dedicated account and restrict process inspection access. Do not put
secret values in repository URLs, checked-in configuration, or reference
environment variables.

Build with the module's Go 1.23-compatible toolchain:

```sh
go build -o ./bin/reksicherung-mcp-restic ./cmd/reksicherung-mcp-restic
```

## Airon stdio example

Airon expands `${NAME}` placeholders before starting MCP children. Configure
the OpenBao bootstrap token in Airon's protected environment, disable inherited
environment passing, and keep prompt-input logging disabled because snapshot
IDs and hostnames can be operationally sensitive.

```yaml
runtime:
  log_inputs: false

mcp_servers:
  - name: reksicherung_restic
    transport: stdio
    command: /usr/local/bin/reksicherung-mcp-restic
    env_passthrough: false
    env:
      REKSICHERUNG_RESTIC_REPOSITORY: ${REKSICHERUNG_RESTIC_REPOSITORY}
      REKSICHERUNG_RESTIC_PASSWORD_REF: ${REKSICHERUNG_RESTIC_PASSWORD_REF}
      REKSICHERUNG_OPENBAO_ADDR: ${REKSICHERUNG_OPENBAO_ADDR}
      REKSICHERUNG_OPENBAO_TOKEN: ${REKSICHERUNG_OPENBAO_TOKEN}
      REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF: ${REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF}
      REKSICHERUNG_RESTIC_AWS_SECRET_ACCESS_KEY_REF: ${REKSICHERUNG_RESTIC_AWS_SECRET_ACCESS_KEY_REF}
      AWS_DEFAULT_REGION: ${AWS_DEFAULT_REGION}

prompts:
  - id: discover_restore_points
    system: |
      Use the Restic tools only to discover available restore-point metadata.
      Ask for an explicit snapshot ID selection before handing off to a
      separately authorized restore workflow. Do not request repository files.
    content: |
      List recent snapshots and summarize the available metadata.
    tools:
      - reksicherung_restic__restic_list_snapshots
      - reksicherung_restic__restic_select_snapshot
```
