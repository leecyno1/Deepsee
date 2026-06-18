#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"
LOG_FILE="${CHATLOG_GRAY_LOG_FILE:-/private/tmp/chatlog_5031.log}"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

if [[ -z "${CHATLOG_BIN:-}" ]]; then
  if [[ -x "$ROOT_DIR/.local/chatlog_0.0.31_darwin_arm64/chatlog" ]]; then
    CHATLOG_BIN="$ROOT_DIR/.local/chatlog_0.0.31_darwin_arm64/chatlog"
  else
    CHATLOG_BIN="$(command -v chatlog 2>/dev/null || true)"
  fi
fi
CHATLOG_DATA_DIR="${CHATLOG_DATA_DIR:-}"
CHATLOG_WORK_DIR="${CHATLOG_WORK_DIR:-${CHATLOG_DIR:-}}"
CHATLOG_GRAY_PORT="${CHATLOG_GRAY_PORT:-5031}"
CHATLOG_PLATFORM="${CHATLOG_PLATFORM:-darwin}"
CHATLOG_VERSION="${CHATLOG_VERSION:-4}"

if [[ ! -x "$CHATLOG_BIN" ]]; then
  echo "chatlog binary not found: $CHATLOG_BIN" >> "$LOG_FILE"
  exit 1
fi
if [[ -z "$CHATLOG_DATA_DIR" || -z "$CHATLOG_WORK_DIR" ]]; then
  echo "missing CHATLOG_DATA_DIR or CHATLOG_WORK_DIR" >> "$LOG_FILE"
  exit 1
fi

mkdir -p "$CHATLOG_WORK_DIR" "$(dirname "$LOG_FILE")"

args=(
  server
  --addr "127.0.0.1:$CHATLOG_GRAY_PORT"
  --platform "$CHATLOG_PLATFORM"
  --version "$CHATLOG_VERSION"
  --data-dir "$CHATLOG_DATA_DIR"
  --work-dir "$CHATLOG_WORK_DIR"
)

if [[ -n "${CHATLOG_DATA_KEY:-}" ]]; then
  args+=(--data-key "$CHATLOG_DATA_KEY")
fi
if [[ -n "${CHATLOG_IMG_KEY:-}" ]]; then
  args+=(--img-key "$CHATLOG_IMG_KEY")
fi
if [[ "${CHATLOG_AUTO_DECRYPT:-0}" == "1" || "${CHATLOG_AUTO_DECRYPT:-false}" == "true" ]]; then
  args+=(--auto-decrypt)
fi

exec "$CHATLOG_BIN" "${args[@]}" >> "$LOG_FILE" 2>&1
