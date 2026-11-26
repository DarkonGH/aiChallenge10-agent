#!/usr/bin/env bash
# Build and start the CLI agent. Needs LLM_BASE_URL and OPENAI_API_KEY.
set -euo pipefail
cd "$(dirname "$0")"

# Fall back to ~/.env when the caller has not exported the credentials.
if [ -z "${LLM_BASE_URL:-}" ] || [ -z "${OPENAI_API_KEY:-}" ]; then
  if [ -f "$HOME/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$HOME/.env"
    set +a
  fi
fi

# Compaction and invariant checks: use local Ollama when it is up. Chat stays
# on the paid API. If LLM_SUMMARY_BASE_URL is set explicitly, that wins; if
# Ollama is down, the summary client falls back to the chat client.
if [ -z "${LLM_SUMMARY_BASE_URL:-}" ] &&
   curl -fsS --max-time 2 http://localhost:11434/api/tags >/dev/null 2>&1; then
  export LLM_SUMMARY_BASE_URL=http://localhost:11434/v1
  export LLM_SUMMARY_API_KEY=${LLM_SUMMARY_API_KEY:-ollama}
  # The endpoint is local, so the model must be local too. ~/.env often exports
  # LLM_SUMMARY_MODEL with the cloud chat id (opencode-go/deepseek-v4.1-flash);
  # Ollama then answers 404 and the profile/memory extractor dies on every turn.
  # Choosing the endpoint here also means choosing its model, so this is not a
  # default that an inherited cloud id can override.
  export LLM_SUMMARY_MODEL=qwen2.5:14b
  export LLM_INVARIANT_MODEL=${LLM_INVARIANT_MODEL:-$LLM_SUMMARY_MODEL}
fi

go build -o agent .
mkdir -p bin
go build -o bin/mcp-cbr ./cmd/mcp-cbr
go build -o bin/mcp-forex ./cmd/mcp-forex
exec ./agent "$@"
