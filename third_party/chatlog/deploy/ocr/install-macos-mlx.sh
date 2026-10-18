#!/bin/zsh
set -euo pipefail

SCRIPT_DIR=${0:A:h}
RUNTIME_ROOT=${CHATLOG_OCR_RUNTIME_DIR:-"$HOME/.chatlog/ocr-runtime"}
MLX_ENV="$RUNTIME_ROOT/venv-mlx"
SDK_ENV="$RUNTIME_ROOT/venv-sdk"
SOURCE_DIR="$RUNTIME_ROOT/src/glm-ocr"
RUN_DIR="$RUNTIME_ROOT/run"
STATUS_FILE="$RUN_DIR/status.json"

mkdir -p "$RUNTIME_ROOT/src" "$RUN_DIR" "$RUNTIME_ROOT/logs"

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

fail() {
  write_status "error" "本地 OCR 运行环境安装失败"
}
trap fail ERR

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  write_status "error" "MLX 自动部署仅用于 Apple Silicon Mac"
  print -u2 "MLX automatic deployment requires Apple Silicon macOS"
  exit 1
fi

UV_BIN=${CHATLOG_OCR_UV_BIN:-}
if [[ -z "$UV_BIN" ]]; then
  UV_BIN=$(command -v uv 2>/dev/null || true)
fi
if [[ -z "$UV_BIN" && -x "$HOME/.local/bin/uv" ]]; then
  UV_BIN="$HOME/.local/bin/uv"
fi
if [[ -z "$UV_BIN" ]]; then
  write_status "error" "缺少 uv Python 环境管理器"
  print -u2 "uv is required"
  exit 1
fi

if [[ ! -x "$MLX_ENV/bin/python" ]]; then
  write_status "installing" "正在创建 MLX Python 3.12 环境"
  "$UV_BIN" venv --python 3.12 "$MLX_ENV"
fi
if [[ ! -x "$MLX_ENV/bin/mlx_vlm.server" ]]; then
  write_status "installing" "正在安装 Apple Silicon MLX 推理服务"
  "$UV_BIN" pip install --python "$MLX_ENV/bin/python" \
    "git+https://github.com/Blaizzy/mlx-vlm.git"
fi

if [[ ! -d "$SOURCE_DIR/.git" ]]; then
  write_status "installing" "正在获取 GLM-OCR SDK"
  rm -rf "$SOURCE_DIR"
  git clone --depth 1 https://github.com/zai-org/GLM-OCR.git "$SOURCE_DIR"
fi

if [[ ! -x "$SDK_ENV/bin/python" ]]; then
  write_status "installing" "正在创建 GLM-OCR SDK Python 3.12 环境"
  "$UV_BIN" venv --python 3.12 "$SDK_ENV"
fi
if ! "$SDK_ENV/bin/python" -c 'import glmocr, flask' >/dev/null 2>&1; then
  write_status "installing" "正在安装 GLM-OCR 布局分析与 HTTP 服务"
  "$UV_BIN" pip install --python "$SDK_ENV/bin/python" \
    -e "${SOURCE_DIR}[selfhosted,server]"
  "$UV_BIN" pip install --python "$SDK_ENV/bin/python" \
    "git+https://github.com/huggingface/transformers.git"
fi

cp "$SCRIPT_DIR/config.macos-mlx.yaml" "$RUNTIME_ROOT/config.yaml"
write_status "installed" "本地 OCR 运行环境已安装"
print "GLM-OCR MLX runtime installed at $RUNTIME_ROOT"
