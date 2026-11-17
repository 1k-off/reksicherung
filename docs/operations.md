# Operations runbook

## Production readiness checklist

1. Complete a lab dry-run via `docs/getting-started.md` (or Airon onboard), then
   harden with `docs/inputs.md` and `.agents/skills/reksicherung-bootstrap/SKILL.md`.
   Compose commands: `examples/compose/RUNBOOK.md`.
2. Pull or promote MCP/CLI images from GHCR with pinned digests. Build from
   `deploy/docker/` only when developing this repository.
3. Deploy OpenBao with persistent storage, unseal strategy, sealed backups, and a
   least-privilege token (or AppRole) for Reksicherung only. Write
   `secret/reksicherung/defaults`, per-source KV, and
   `secret/reksicherung/sqlserver` (`HOST`/`USERNAME`/`PASSWORD`, optional
   `PORT`) per `docs/schemas/openbao-source-kv.md`.
4. Deploy an isolated SQL Server restore instance (and/or MySQL for
   `database.type: mysql`) on a network that cannot reach production databases;
   create a dedicated restore login/account matching OpenBao
   (`secret/reksicherung/sqlserver` / `secret/reksicherung/mysql`). The MySQL
   account must be limited to `reksicherung\_%` databases (no `FILE`/`SUPER`).
5. Mount shared artifact and backup volumes into provider + restore MCP processes.
6. Run `cleanup-reconciler` continuously against the run registry volume.
7. Point any orchestrator (or manual operator workflow) at MCP servers with
   OpenBao bootstrap env only, plus thin `BackupCatalog` / inventory — no
   per-backup S3 keys in compose `.env`. Optional Airon example:
   `examples/airon/`.
8. Validate with a synthetic backup E2E (`go test -tags=e2e ./test/e2e/...` when
   fixtures/services are available) before enabling schedules.

## OpenBao

The compose reference uses file storage and TLS disabled for first bring-up on a
private network. Before production:

- enable TLS or terminate TLS at a reverse proxy
- define unseal (Shamir or auto-unseal) and sealed-state recovery drills
- enable KV v2 at mount `secret`
- write only the keys referenced by the catalog / MCP env
- issue a short-lived renewable token or AppRole for MCP processes
- never use `-dev` or a root token in production

Token renewal is not implemented inside MCP processes; provision a token valid
for the process lifetime and restart processes on rotation.

## Evidence and audit

- Orchestrator logs (if any): retain as orchestration evidence.
- Reksicherung evidence: JSON Lines via `internal/reksicherung/evidence.Writer`
  under an operator-managed path such as
  `/var/lib/reksicherung/evidence/<run-id>.jsonl` (mode 0600).
- Correlate by Reksicherung run ID; store optional `external_run_id` when the
  orchestrator supplies MCP `_meta` `1node.xyz/external-run-id` or the tool arg.
- Retention: operator policy (recommended ≥ 90 days for restore-check evidence).
- Redaction: no secrets, DSNs, or unnecessary row-level business data.

## Cleanup

| Mode | Behavior |
| --- | --- |
| `delete` | Drop run-scoped DB after the run; reconciler retries on lease expiry |
| `hold_for_review` | Keep DB; register with `reksicherung hold -database-type sqlserver\|mysql`, later `reksicherung cleanup` |

A held run **must** be registered with `reksicherung hold` (explicit
`-database-type`); a record without `database_type` is treated as `sqlserver`.

Reconciler routes drops by the recorded `database_type` (`sqlserver` via
`sqlcmd`, `mysql` via the `mysql` client; both in the `cli` image) and never
stops SQL Server, MySQL, or MCP services. Failed cleanup is visible in
registry status and stderr; it is not reported as a clean PASS.

## Integrity wording

`command_completed` from `sqlserver_check_restore_integrity` /
`mysql_check_restore_integrity` (and the inspect `*_check_integrity` tools)
means only that the fixed check command exited without error. It is not a
clean-integrity verdict; evidence and reports should say "check completed",
never "clean", and a failed or skipped check is `FAIL`/`INCONCLUSIVE`.

## hold_for_review access

- Restrict SQL Server / MySQL network to operator jump hosts / break-glass identities.
- Record retained database name (`reksicherung_<hash>`) in evidence.
- Set an organizational TTL (recommended ≤ 7 days) and track via tickets.
- Drop with `reksicherung cleanup -registry ... -run-id ...` after review.

## Image provenance

- Development may use mutable tags (`2022-latest`).
- Production: pin digest for SQL Server, MySQL, OpenBao, and Reksicherung images.
- Rebuild MCP images on dependency CVEs; keep SqlPackage/sqlcmd/restic versions
  recorded in image labels or release notes.
