# Prompt: run restore-check for all latest sources

Follow `skills/reksicherung-restore-check/SKILL.md` and `docs/schemas/restore-check-prompt.md`.

Slots:

- `inventory`: paste `I-INVENTORY` JSON from `reksicherung catalog`
- `cleanup_mode`: `delete` unless told otherwise

For each inventory entry, call provider MCP tools with that entry's `id` as
`source_id`. Aggregate verdicts; one failed required check → overall FAIL.
