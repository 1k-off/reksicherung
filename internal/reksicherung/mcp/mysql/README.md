# MySQL semantic-review service

Read-only inspection of a restored MySQL database, mirroring
[`../sqlserver`](../sqlserver). Four operations:

- `ListTables` returns a capped inventory of base tables from
  `information_schema.tables`.
- `DescribeTable` returns capped column metadata from
  `information_schema.columns`.
- `Validate` counts rows in one table, capped at a limit, and returns only the
  capped count plus a truncation flag. It accepts **no predicates** (the SQL
  Server service takes none either), so repeated value-based counts cannot
  become a row-value oracle.
- `CheckIntegrity` runs fixed, default-option `CHECK TABLE` on each listed base
  table of the target database and reports command completion only.

## Scoping and quoting

`Options.TargetDatabase` scopes all operations to one database. In MySQL a
schema *is* a database, so `TableRef.Schema` is optional: empty means the
target, anything else must equal `Database`. Identifiers are backtick-quoted
(embedded backticks doubled; empty, NUL, control-character, or >64-character
names rejected). Metadata filters and limits are bound parameters.

MCP tool servers additionally protocol-encode identifier strings in model-facing
output (`internal/reksicherung/mcp/protocol`): only `[A-Za-z_][A-Za-z0-9_]*`
literals are emitted plain; everything else becomes an opaque `b64:` token so
crafted dump names cannot be surfaced as natural-language instructions.

If the target is `information_schema` (`AnchorDatabase`, used when the process is
not pinned to one database), the service refuses to inspect it and
`ForDatabase` re-scopes it only to run-scoped names matching
`reksicherung_<32 hex>`, the same names the MySQL restore MCP creates via
`DatabaseNameForRun`.

## Integrity: completion only

`CHECK TABLE` reports table problems as **result rows** (`Msg_type`,
`Msg_text`), not as command errors. The service drains and discards those rows.
`IntegrityResult.CommandCompleted` therefore means only that every `CHECK TABLE`
statement ran without a command/driver error; it is **not** a clean-integrity
verdict. If the table inventory exceeds `MaxTables`, only that many tables are
checked, `Truncated` is true, and `CommandCompleted` is false. No `REPAIR`,
`OPTIMIZE`, `ANALYZE`, or `CHECK TABLE` option (`EXTENDED`, `FOR UPGRADE`, ...)
is ever issued.

## Privileges and limits

The service never issues data-changing SQL, and it runs in autocommit mode.
Enforce the boundary in MySQL: grant a dedicated account `SELECT` on the target
database; `CHECK TABLE` additionally requires `INSERT` on the checked tables.
Use that account only against an isolated, disposable restored database, and
grant no DDL, `DROP`, `FILE`, `SUPER`, or grant privileges. Row/table caps and
timeouts bound returned results, not server CPU or I/O.

Errors are reduced to stable generic sentinels; the service never logs SQL,
parameters, credentials, or driver errors.

## Local integration check

Opt-in and loopback-only. Run against a disposable MySQL you started yourself,
with a dummy database; supply the DSN via `REKSICHERUNG_MYSQL_TEST_DSN`
(`user:pass@tcp(127.0.0.1:3306)/dbname`). Never use a production DSN.

```sh
REKSICHERUNG_MYSQL_TEST_DSN="$LOCAL_MYSQL_DSN" \
  go test -tags=integration ./internal/reksicherung/mcp/mysql
```

Default `go test` uses fakes only and needs no MySQL server.
