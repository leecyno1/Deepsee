#!/bin/zsh
set -euo pipefail

SCRIPT_DIR=${0:A:h}
RUNTIME_ROOT=${CHATLOG_OCR_RUNTIME_DIR:-"$HOME/.chatlog/ocr-runtime"}
MLX_ENV="$RUNTIME_ROOT/venv-mlx"
SDK_ENV="$RUNTIME_ROOT/venv-sdk"
RUN_DIR="$RUNTIME_ROOT/run"
LOG_DIR="$RUNTIME_ROOT/logs"
STATUS_FILE="$RUN_DIR/status.json"
CONFIG_FILE="$RUNTIME_ROOT/config.yaml"
SKIP_INSTALL=false
[[ "${1:-}" == "--skip-install" ]] && SKIP_INSTALL=true

# Xet opens many parallel range connections and can stall behind local proxy
# tunnels. The regular Hugging Face download path resumes partial files and is
# more reliable for the one-time GLM-OCR model download on macOS.
export HF_HUB_DISABLE_XET=${HF_HUB_DISABLE_XET:-1}
export HF_HUB_DOWNLOAD_TIMEOUT=${HF_HUB_DOWNLOAD_TIMEOUT:-600}

mkdir -p "$RUN_DIR" "$LOG_DIR"

write_status() {
  local state=$1
  local message=$2
  python3 - "$STATUS_FILE" "$state" "$message" <<'PY'
import json, os, sys, time
path, state, message = sys.argv[1:]
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as handle:
    json.dump({"state": state, "message": message, "updated_at": int(time.time())}, handle, ensure_ascii=False)
os.replace(tmp, path)
PY
}

rotate_managed_log() {
  local log_path=$1
  python3 - "$log_path" "${CHATLOG_OCR_LOG_MAX_MB:-20}" "${CHATLOG_OCR_LOG_BACKUPS:-4}" <<'PY'
import datetime, gzip, os, pathlib, shutil, sys
path = pathlib.Path(sys.argv[1])
try:
    max_bytes = max(1, int(sys.argv[2])) * 1024 * 1024
    backups = max(1, int(sys.argv[3]))
except ValueError:
    max_bytes, backups = 20 * 1024 * 1024, 4
try:
    size = path.stat().st_size
except FileNotFoundError:
    size = 0
if size >= max_bytes:
    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    rotated = path.with_name(path.name + "." + stamp)
    os.replace(path, rotated)
    compressed = pathlib.Path(str(rotated) + ".gz")
    with rotated.open("rb") as source, gzip.open(compressed, "wb", compresslevel=6) as target:
        shutil.copyfileobj(source, target, length=1024 * 1024)
    rotated.unlink(missing_ok=True)
items = sorted(path.parent.glob(path.name + ".*.gz"), key=lambda item: item.stat().st_mtime, reverse=True)
for item in items[backups:]:
    item.unlink(missing_ok=True)
PY
}

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
    *) return 1 ;;
  esac
}

port_open() {
  /usr/bin/nc -z 127.0.0.1 "$1" >/dev/null 2>&1
}

backend_healthy() {
  port_open 8080
}

sdk_healthy() {
  /usr/bin/curl -fsS --max-time 2 http://127.0.0.1:8081/health >/dev/null 2>&1
}

stop_pair() {
  local pids=()
  local file kind pid
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
    local alive=false
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
}

if pid_alive "$RUN_DIR/mlx.pid" mlx && pid_alive "$RUN_DIR/sdk.pid" sdk && backend_healthy && sdk_healthy; then
  write_status "ready" "MLX 与 GLM-OCR SDK 已同步运行"
  exit 0
fi

if ! mkdir "$RUN_DIR/start.lock" 2>/dev/null; then
  lock_pid=$(cat "$RUN_DIR/start.lock/pid" 2>/dev/null || true)
  if [[ "$lock_pid" == <-> ]] && kill -0 "$lock_pid" 2>/dev/null; then
    # Another starter owns installation/warm-up.
    exit 0
  fi
  rm -rf "$RUN_DIR/start.lock"
  mkdir "$RUN_DIR/start.lock"
fi
print $$ >"$RUN_DIR/start.lock/pid"
cleanup_start() {
  local code=$?
  trap - EXIT
  if (( code != 0 )); then
    stop_pair
  fi
  rm -rf "$RUN_DIR/start.lock"
  return $code
}
trap cleanup_start EXIT

# A healthy SDK without its MLX backend is a split state. Stop the owned pair
# before rebuilding it so a manual start always ends with both processes ready.
if sdk_healthy && ! backend_healthy; then
  stop_pair
fi

if [[ ! -x "$MLX_ENV/bin/mlx_vlm.server" || ! -x "$SDK_ENV/bin/python" || ! -f "$CONFIG_FILE" ]]; then
  if $SKIP_INSTALL; then
    write_status "error" "本地 OCR 运行环境尚未安装"
    exit 1
  fi
  "$SCRIPT_DIR/install-macos-mlx.sh"
fi
# Keep the managed runtime aligned with the configuration shipped by Chatlog.
cp "$SCRIPT_DIR/config.macos-mlx.yaml" "$CONFIG_FILE"

if ! pid_alive "$RUN_DIR/mlx.pid" mlx; then
  rm -f "$RUN_DIR/mlx.pid"
  rotate_managed_log "$LOG_DIR/mlx.log"
  write_status "starting_backend" "正在启动 MLX Metal 推理服务"
  nohup "$MLX_ENV/bin/mlx_vlm.server" \
    --trust-remote-code \
    --host 127.0.0.1 \
    --port 8080 \
    >>"$LOG_DIR/mlx.log" 2>&1 </dev/null &
  print $! >"$RUN_DIR/mlx.pid"
  disown $(cat "$RUN_DIR/mlx.pid") 2>/dev/null || true
fi

for _ in {1..300}; do
  backend_healthy && break
  if ! pid_alive "$RUN_DIR/mlx.pid" mlx; then
    write_status "error" "MLX 推理服务启动失败，请查看 mlx.log"
    exit 1
  fi
  sleep 1
done
if ! backend_healthy; then
  write_status "error" "MLX 推理服务启动超时"
  exit 1
fi

write_status "warming_backend" "正在下载并加载 GLM-OCR MLX 模型"
if ! /usr/bin/curl -fsS --max-time 1800 \
  http://127.0.0.1:8080/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mlx-community/GLM-OCR-bf16","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_tokens":1}' \
  >"$RUN_DIR/mlx-warmup.json"; then
  write_status "error" "GLM-OCR MLX 模型加载失败，请查看 mlx.log"
  exit 1
fi

if ! pid_alive "$RUN_DIR/sdk.pid" sdk; then
  rm -f "$RUN_DIR/sdk.pid"
  rotate_managed_log "$LOG_DIR/sdk.log"
  write_status "starting_sdk" "正在启动 GLM-OCR 布局分析服务"
  nohup "$SDK_ENV/bin/python" -m glmocr.server \
    --config "$CONFIG_FILE" \
    >>"$LOG_DIR/sdk.log" 2>&1 </dev/null &
  print $! >"$RUN_DIR/sdk.pid"
  disown $(cat "$RUN_DIR/sdk.pid") 2>/dev/null || true
fi

write_status "warming" "正在加载 GLM-OCR 与版面分析模型"
for _ in {1..600}; do
  if sdk_healthy; then
    if pid_alive "$RUN_DIR/mlx.pid" mlx && pid_alive "$RUN_DIR/sdk.pid" sdk && backend_healthy; then
      write_status "ready" "MLX 与 GLM-OCR SDK 已同步运行"
      exit 0
    fi
    write_status "error" "GLM-OCR SDK 已启动，但 MLX 推理后端未就绪"
    exit 1
  fi
  if ! pid_alive "$RUN_DIR/sdk.pid" sdk; then
    write_status "error" "GLM-OCR SDK 服务启动失败，请查看 sdk.log"
    exit 1
  fi
  sleep 1
done

write_status "error" "GLM-OCR 服务预热超时"
exit 1
