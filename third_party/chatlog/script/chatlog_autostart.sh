#!/bin/zsh
set -euo pipefail

REPO_DIR="/Volumes/PSSD/Projects/chatlog_alpha"
CHATLOG_BIN="$REPO_DIR/chatlog_0.0.29_darwin_arm64_副本3/chatlog"
DATA_DIR="/Users/lichengyin/Library/Containers/com.tencent.xinWeChat/Data/Documents/xwechat_files/leecyno1_84c7"
ADDR="127.0.0.1:5030"
LOG_FILE="/tmp/chatlog_5030.log"
WORK_ROOT="/Users/lichengyin/Documents/chatlog"
WORK_PID_FILE="$WORK_ROOT/chatlog.pid"

if [[ ! -x "$CHATLOG_BIN" ]]; then
  echo "chatlog binary not found: $CHATLOG_BIN" >> "$LOG_FILE"
  exit 1
fi

mkdir -p "$WORK_ROOT"

# Remove stale single-instance pid file to avoid non-interactive startup blocking.
if [[ -f "$WORK_PID_FILE" ]]; then
  OLD_PID="$(cat "$WORK_PID_FILE" 2>/dev/null || true)"
  if [[ -n "${OLD_PID:-}" ]] && ! kill -0 "$OLD_PID" 2>/dev/null; then
    rm -f "$WORK_PID_FILE"
  fi
fi

# Ensure target port is free before start.
PORT_PID="$(lsof -nP -tiTCP:5030 -sTCP:LISTEN 2>/dev/null || true)"
if [[ -n "${PORT_PID:-}" ]]; then
  kill -TERM "$PORT_PID" 2>/dev/null || true
  sleep 1
fi

# Pre-decrypt once, then keep incremental updates via auto-decrypt.
"$CHATLOG_BIN" decrypt -d "$DATA_DIR" >> "$LOG_FILE" 2>&1 || true

exec "$CHATLOG_BIN" server --addr "$ADDR" --data-dir "$DATA_DIR" --auto-decrypt >> "$LOG_FILE" 2>&1
