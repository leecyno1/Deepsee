#!/bin/zsh
set -euo pipefail

RUNTIME_ROOT=${CHATLOG_OCR_RUNTIME_DIR:-"$HOME/.chatlog/ocr-runtime"}
RUN_DIR="$RUNTIME_ROOT/run"
STATUS_FILE="$RUN_DIR/status.json"

pid_alive() {
  local file=$1
  local kind=$2
  [[ -f "$file" ]] || return 1
  local pid command
  pid=$(cat "$file" 2>/dev/null || true)
  [[ "$pid" == <-> ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  command=$(ps -p "$pid" -o command= 2>/dev/null || true)
  case "$kind" in
    mlx) [[ "$command" == *"$RUNTIME_ROOT"* && "$command" == *"mlx_vlm.server"* ]] ;;
    sdk) [[ "$command" == *"$RUNTIME_ROOT"* && "$command" == *"glmocr.server"* ]] ;;
    start) [[ "$command" == *"start-macos-mlx.sh"* ]] ;;
    *) return 1 ;;
  esac
}

start_pid_file="$RUN_DIR/start.lock/pid"
if pid_alive "$start_pid_file" start; then
  start_pid=$(cat "$start_pid_file")
  kill -TERM "$start_pid" 2>/dev/null || true
fi

# Signal both members first, then wait for both. This prevents the SDK or MLX
# backend remaining alive while the other member is being shut down.
pids=()
append_descendants() {
  local parent=$1 child
  for child in $(pgrep -P "$parent" 2>/dev/null || true); do
    pids+=("$child")
    append_descendants "$child"
  done
}
for file kind in "$RUN_DIR/sdk.pid" sdk "$RUN_DIR/mlx.pid" mlx; do
  if pid_alive "$file" "$kind"; then
    pid=$(cat "$file")
    pids+=("$pid")
    append_descendants "$pid"
  fi
done
(( ${#pids[@]} )) && kill -TERM $pids 2>/dev/null || true
for _ in {1..30}; do
  alive=false
  for pid in $pids; do
    if kill -0 "$pid" 2>/dev/null; then
      alive=true
      break
    fi
  done
  $alive || break
  sleep 0.2
done
(( ${#pids[@]} )) && kill -KILL $pids 2>/dev/null || true
rm -f "$RUN_DIR/sdk.pid" "$RUN_DIR/mlx.pid"
rm -rf "$RUN_DIR/start.lock"
mkdir -p "$RUN_DIR"
python3 - "$STATUS_FILE" <<'PY'
import json, os, sys, time
path = sys.argv[1]
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as handle:
    json.dump({"state": "stopped", "message": "MLX 与 GLM-OCR SDK 已同步停止", "updated_at": int(time.time())}, handle, ensure_ascii=False)
os.replace(tmp, path)
PY
print "MLX and GLM-OCR SDK stopped together"
