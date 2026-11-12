# Reksicherung bootstrap

Bring up a lab/production restore-check stack from an empty host using only
documented inputs (`docs/inputs.md`).

## Inputs

- `I-COMPOSE-ENV` — `examples/compose/.env` (bootstrap only)
- `I-OPENBAO-DEFAULTS`, `I-OPENBAO-SQL` — after OpenBao init
- `I-CATALOG` — at least one source (may add later via add-source skill)
- Optional `I-ORCHESTRATOR-JOB` — job YAML for an MCP/skills runner (see
  `examples/airon/` for one binding)

## Steps

1. Copy `examples/compose/.env.example` → `.env`; set SA password and OpenBao
   addr/token placeholders only.
2. `docker compose -f examples/compose/docker-compose.yml build`
3. `docker compose ... up -d openbao sqlserver`
4. Init/unseal OpenBao; enable KV v2 at `secret`; write `I-OPENBAO-SQL`
   (`HOST`, `USERNAME`, `PASSWORD`; optional `PORT`) and optional
   `I-OPENBAO-DEFAULTS` per `docs/schemas/openbao-source-kv.md`.
5. Create SQL restore login matching OpenBao `USERNAME`/`PASSWORD`.
6. Put least-privilege token into `.env`; start `cleanup-reconciler`.
7. Build MCP/CLI binaries (or images). Host MCP must use loopback OpenBao HTTP
   or HTTPS for non-loopback.
8. Write thin `I-CATALOG`; run `reksicherung validate` + `reksicherung catalog`.
9. Optionally configure an orchestrator job (`I-ORCHESTRATOR-JOB`) or run MCP
   tools manually / via other automation.
10. Do not enable schedules until a dry-run PASS (restore-check skill).

## Fail closed

- Missing required compose keys or OpenBao fields → stop.
- Per-backup S3 keys in `.env` → remove; use OpenBao sources instead.
