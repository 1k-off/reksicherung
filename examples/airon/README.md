# Optional Airon bindings

Airon is **not** required to run Reksicherung. These files show one orchestrator
binding of `I-ORCHESTRATOR-JOB`.

| File | Audience | Notes |
| --- | --- | --- |
| [`restore-check.yaml`](restore-check.yaml) | Developers with **host-built** MCP binaries on `PATH` / fixed paths | Stub for local binary wiring. Uses `env_passthrough: false` and `/usr/local/bin/…`. **Do not** use this as the Windows/GHCR lab recipe. |
| [`integration-follow-up.md`](integration-follow-up.md) | Airon maintainers | Correlation / log-handoff ideas for the Airon repo only |

## Recommended lab path

For a client/lab package with **GHCR images**, `$RUNTIME run --rm -i`, and
Windows/Podman pitfalls applied:

1. Skill [`skills/reksicherung-airon-onboard/SKILL.md`](../../skills/reksicherung-airon-onboard/SKILL.md)
2. Templates under `skills/reksicherung-airon-onboard/templates/` (especially
   `airon-mcp-stdio.yaml` and `runbook-skeleton.md`)
3. Pitfalls: [`skills/reksicherung-airon-onboard/reference-pitfalls.md`](../../skills/reksicherung-airon-onboard/reference-pitfalls.md)

Without Airon: [`docs/getting-started.md`](../../docs/getting-started.md) and
[`docs/mcp-clients.md`](../../docs/mcp-clients.md).
