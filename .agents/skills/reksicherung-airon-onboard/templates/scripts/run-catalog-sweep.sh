#!/usr/bin/env bash
# Daily catalog coverage sweep (Airon only).
# Assumes OpenBao + SQL Server (+ reconciler) are already up and unsealed.
#
#   OPENAI_API_KEY=… ./scripts/run-catalog-sweep.sh
#
# Cron:
#   0 6 * * * /path/to/package/scripts/run-catalog-sweep.sh >>…/logs/cron-sweep.log 2>&1
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
JOB_DIR="$PKG_ROOT/job"
COMPOSE_ENV="$PKG_ROOT/compose/.env"

AIRON_BIN="${AIRON_BIN:-}"
if [[ -z "$AIRON_BIN" ]]; then
  if command -v airon >/dev/null 2>&1; then
    AIRON_BIN="$(command -v airon)"
  else
    echo "airon not found; set AIRON_BIN or put airon on PATH" >&2
    exit 1
  fi
fi

if [[ -z "${REKSICHERUNG_OPENBAO_TOKEN:-}" && -f "$COMPOSE_ENV" ]]; then
  set -a
  # shellcheck disable=SC2046
  eval "$(grep -E '^REKSICHERUNG_OPENBAO_TOKEN=' "$COMPOSE_ENV" | sed 's/\r$//')"
  set +a
fi

if [[ -z "${REKSICHERUNG_OPENBAO_TOKEN:-}" || "$REKSICHERUNG_OPENBAO_TOKEN" == replace* ]]; then
  echo "REKSICHERUNG_OPENBAO_TOKEN missing — unseal OpenBao / check compose/.env" >&2
  exit 1
fi

if [[ -z "${OPENAI_API_KEY:-}" && -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "Set OPENAI_API_KEY (or ANTHROPIC_API_KEY) for the cron user" >&2
  exit 1
fi

DAILY_BATCH="${DAILY_BATCH:-2}"
OLDEST_WINDOW="${OLDEST_WINDOW:-10}"
CLEANUP="${CLEANUP:-delete}"

INPUT="cleanup=${CLEANUP}
daily_batch=${DAILY_BATCH}
oldest_window=${OLDEST_WINDOW}"

cd "$JOB_DIR"
exec "$AIRON_BIN" run \
  --config ./airon-catalog-sweep.yaml \
  --input "$INPUT"
