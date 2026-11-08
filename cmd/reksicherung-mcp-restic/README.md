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

The executable reads these process environment variables:

- `REKSICHERUNG_RESTIC_REPOSITORY`: the single Restic repository URL/path.
- `REKSICHERUNG_RESTIC_PASSWORD`: the repository password.

For supported remote backends, the process may also receive explicit backend
credentials: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`,
`AWS_DEFAULT_REGION`, `AWS_REGION`, `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY`,
`AZURE_ACCOUNT_NAME`, or `AZURE_ACCOUNT_KEY`. Only those allowlisted variables
are passed to the Restic child process, alongside `RESTIC_REPOSITORY` and
`RESTIC_PASSWORD`; the rest of the parent environment is not inherited.
Variables such as `RESTIC_PASSWORD_COMMAND` are intentionally not supported.

The server currently accepts secrets only through its protected process
environment. It does not resolve provider-neutral/OpenBao references: this
standalone process has no configured secret-provider lifecycle or token
renewal/cleanup contract. Supply values through Airon's protected environment
or another secret manager's process-injection mechanism. Do not place literal
secrets in checked-in configuration. Environment variables can be visible to
other processes running under the same OS account, so use a dedicated account
and restrict process inspection access. No credentials or tool inputs are
logged.

Build with the module's Go 1.23-compatible toolchain:

```sh
go build -o ./bin/reksicherung-mcp-restic ./cmd/reksicherung-mcp-restic
```

## Airon stdio example

Airon expands `${NAME}` placeholders before starting MCP children. Configure
the values in Airon's protected environment, disable inherited environment
passing, and keep prompt-input logging disabled because snapshot IDs and
hostnames can be operationally sensitive.

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
      REKSICHERUNG_RESTIC_PASSWORD: ${REKSICHERUNG_RESTIC_PASSWORD}
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY}
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
