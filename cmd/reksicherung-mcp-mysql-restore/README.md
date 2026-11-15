# MySQL restore MCP

`reksicherung-mcp-mysql-restore` runs bounded MySQL restore commands against an
**already running, operator-managed MySQL server**. It never starts, stops, or
removes containers, Compose projects, or database services. Use a disposable
MySQL instance/network dedicated to restore checks.

## Tools

- `mysql_restore_backup` accepts an opaque artifact ID of staging kind `sql`
  (a plain mysqldump-style logical dump) plus `source_database`. It creates the
  run-derived database, rewrites `CREATE DATABASE` / `USE` / `DROP DATABASE` and
  `` `source`.`table` `` qualifiers to that name, and streams the dump through
  the `mysql` client. Dumps that reference any other database, contain a second
  `CREATE DATABASE`, or contain `mysql` client commands (`system`, `source`,
  `\!`, ...) fail closed. Seekable staged dumps are fully validated before the
  database is created. If a restore fails part-way, call the drop tool.
- `mysql_check_restore_integrity` runs fixed `mysqlcheck --check`.
  `command_completed: true` means only that the command exited without error;
  it is **not** a clean-integrity verdict.
- `mysql_drop_restore_database` drops only the run-derived database
  (`reksicherung_<hash of run_id>`). It accepts only `run_id`.

There is no arbitrary SQL tool. The MySQL account needs `CREATE`, `DROP`, and
object privileges on `reksicherung\_%` databases only; do not grant access to
production schemas.

## Configuration

Required:

- `REKSICHERUNG_OPENBAO_ADDR` and `REKSICHERUNG_OPENBAO_TOKEN`.

MySQL connection is read from OpenBao `secret/reksicherung/mysql` (`HOST`,
`USERNAME`, `PASSWORD`; optional `PORT`, default `3306`).

Optional:

- `REKSICHERUNG_ARTIFACT_STAGE_DIR`: default `/var/lib/reksicherung/artifacts`
  (shared, pre-created, read access to staged artifacts).
- `REKSICHERUNG_MYSQL`: default `mysql`.
- `REKSICHERUNG_MYSQLCHECK`: default `mysqlcheck`.
- `REKSICHERUNG_MYSQL_TMPDIR`: directory for short-lived `0600` credential
  files; default the OS temp directory.
- `REKSICHERUNG_ARTIFACT_MAX_BYTES`: maximum dump size, default 200 GiB,
  hard-capped at 1 TiB.

## Safety

Passwords are written only to a `0600` `--defaults-extra-file` that is removed
after each command; they never appear in argv, environment, or MCP responses.
Commands use argv (no shell), cap output, and sanitize errors. The client runs
with `--binary-mode --local-infile=0`. The rewriter lexes the dump (strings,
identifiers, comments, statement boundaries) and fails closed on `USE`/database
statements it cannot rewrite, cross-database qualifiers (`other`.`t`, `other.t`;
table-qualified `t`.`c` is also rejected), `PREPARE`/`EXECUTE`, client
commands, and `sql_mode` changes that could desynchronise it. It is not a full
SQL parser; treat the target server as disposable. No live MySQL integration test is implied by the
unit tests. Staged source artifacts are retained and never deleted by this MCP.
