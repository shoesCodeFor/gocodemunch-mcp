#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -z "${VECTOR_BACKEND:-}" ]]; then
  export VECTOR_BACKEND=sqlite
fi

if [[ -z "${OLLAMA_BASE_URL:-}" ]]; then
  export OLLAMA_BASE_URL="http://localhost:11434"
fi

if [[ -z "${VECTOR_QUERY_TIMEOUT_MS:-}" ]]; then
  export VECTOR_QUERY_TIMEOUT_MS="120000"
fi

exec go run ./src/cmd/vector-smoke "$@"
