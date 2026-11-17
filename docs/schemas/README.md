# Schemas

JSON Schema drafts and markdown contracts for every Reksicherung input.

| File | Input ID |
| --- | --- |
| `backup-catalog.v1alpha2.schema.json` | I-CATALOG |
| `backup-inventory.v1alpha2.schema.json` | I-INVENTORY |
| `openbao-defaults.schema.json` | I-OPENBAO-DEFAULTS |
| `openbao-source.schema.json` | I-OPENBAO-SOURCE |
| `openbao-sqlserver.schema.json` | I-OPENBAO-SQL |
| `openbao-mysql.schema.json` | I-OPENBAO-MYSQL |
| `compose-env.schema.json` | I-COMPOSE-ENV |
| `orchestrator-job.schema.json` | I-ORCHESTRATOR-JOB |
| `restore-check-prompt.md` | I-PROMPT |
| `mcp-tools.schema.json` | I-MCP-TOOLS |
| `evidence-event.v1.schema.json` | I-EVIDENCE |
| `configgen-input.schema.json` | I-CONFIGGEN |
| `openbao-source-kv.md` | human/AI convention notes |

Fixtures: `fixtures/valid/` and `fixtures/invalid/` (including `configgen.*.yaml`).

Operator-oriented configgen templates (same contract): [`../../examples/configgen/`](../../examples/configgen/).

See also [`../inputs.md`](../inputs.md), [`../getting-started.md`](../getting-started.md),
[`../glossary.md`](../glossary.md). Optional Airon binding:
[`../../examples/airon/`](../../examples/airon/) (read its README before using
`restore-check.yaml`).
