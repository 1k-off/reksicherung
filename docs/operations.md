# Operations runbook

## Production readiness checklist

1. Build and promote MCP/CLI images from `deploy/docker/` with pinned base digests.
2. Deploy OpenBao with persistent storage, unseal strategy, sealed backups, and a
   least-privilege token (or AppRole) for Reksicherung only.
3. Deploy an isolated SQL Server restore instance on a network that cannot reach
   production databases; create a dedicated restore login.
4. Mount shared artifact and backup volumes into provider + restore MCP processes.
5. Run `cleanup-reconciler` continuously against the run registry volume.
6. Configure Airon with the skill, MCP servers, and protected secret env.
7. Validate with a synthetic backup E2E (`go test -tags=e2e ./test/e2e/...` when
   fixtures/services are available) before enabling cron.

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

- Airon JSON run log: retain as orchestration evidence (steps, tool calls, token
  usage when reported).
- Reksicherung evidence: JSON Lines via `internal/reksicherung/evidence.Writer`
  under an operator-managed path such as
  `/var/lib/reksicherung/evidence/<run-id>.jsonl` (mode 0600).
- Correlate by Reksicherung run ID; store optional Airon run ID when available.
- Retention: operator policy (recommended ≥ 90 days for restore-check evidence).
- Redaction: no secrets, DSNs, or unnecessary row-level business data.

Until Airon injects run IDs via MCP `_meta` (see
`docs/airon-integration-follow-up.md`), pass correlation IDs explicitly in the
workflow plan where possible and keep Airon logs in a durable location Airon
already owns.

## Cleanup

| Mode | Behavior |
| --- | --- |
| `delete` | Drop run-scoped DB after the run; reconciler retries on lease expiry |
| `hold_for_review` | Keep DB; operator runs `reksicherung cleanup` |

Reconciler never stops SQL Server or MCP services. Failed cleanup is visible in
registry status and stderr; it is not reported as a clean PASS.

## hold_for_review access

- Restrict SQL Server network to operator jump hosts / break-glass identities.
- Record retained database name (`reksicherung_<hash>`) in evidence.
- Set an organizational TTL (recommended ≤ 7 days) and track via tickets.
- Drop with `reksicherung cleanup -registry ... -run-id ...` after review.

## Image provenance

- Development may use mutable tags (`2022-latest`).
- Production: pin digest for SQL Server, OpenBao, and Reksicherung images.
- Rebuild MCP images on dependency CVEs; keep SqlPackage/sqlcmd/restic versions
  recorded in image labels or release notes.
