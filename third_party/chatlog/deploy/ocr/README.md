# GLM-OCR self-hosted vLLM deployment

This stack follows the official two-stage production path:

1. vLLM serves `zai-org/GLM-OCR` through the OpenAI-compatible endpoint.
2. The official `glmocr` SDK server performs PP-DocLayout-V3 layout detection,
   parallel region OCR and Markdown/JSON formatting.
3. Chatlog calls `POST /glmocr/parse`, persists results in its own SQLite
   sidecar, and never writes to WeChat databases.

## Apple Silicon Mac: automatic MLX service

On an M-series Mac, Chatlog automatically manages the official MLX deployment
when local OCR is enabled and the endpoint is
`http://127.0.0.1:8081/glmocr/parse`.

The first start runs `./start-macos-mlx.sh`. It creates two isolated Python
3.12 environments under `~/.chatlog/ocr-runtime`, installs `mlx-vlm` and the
GLM-OCR SDK, starts Metal inference on port `8080`, then starts the SDK HTTP
service on port `8081`. Later starts reuse the installed environments and model
caches.

Runtime state and logs:

```text
~/.chatlog/ocr-runtime/run/status.json
~/.chatlog/ocr-runtime/logs/mlx.log
~/.chatlog/ocr-runtime/logs/sdk.log
```

Stop the Mac services with `./stop-macos-mlx.sh`. The Web `OCR 识别` page shows
both process IDs and health states, provides manual start/stop buttons, and
persists the two lifecycle switches:

- `local_auto_start`: start the model stack when local OCR follows Chatlog.
- `local_auto_restart`: restart a previously healthy stack after an unexpected
  process exit.

Environment overrides are `CHATLOG_OCR_LOCAL_AUTO_START=false` and
`CHATLOG_OCR_LOCAL_AUTO_RESTART=false`. Set `CHATLOG_OCR_RUNTIME_DIR` to move
the runtime directory.

## Start on an NVIDIA GPU host

Requirements: Docker, Docker Compose v2, NVIDIA Container Toolkit, and a GPU
visible to `docker run --gpus all`.

```bash
cd deploy/ocr
docker compose -f docker-compose.vllm.yml up -d --build
curl --fail http://127.0.0.1:8081/health
```

macOS 启停脚本将 MLX 与 GLM-OCR SDK 视为同一个进程组：仅两端均就绪时
报告启动成功；启动异常会整组回滚，停止时会同时结束两端及其子进程。

The first start downloads GLM-OCR and PP-DocLayout-V3 weights. Tail both
services while they warm up:

```bash
docker compose -f docker-compose.vllm.yml logs -f vllm sdk
```

Configure Chatlog:

```bash
export CHATLOG_OCR_ENABLED=true
export CHATLOG_OCR_PROVIDER=vllm
export CHATLOG_OCR_ENDPOINT=http://127.0.0.1:8081/glmocr/parse
export CHATLOG_OCR_MODEL=glm-ocr
export CHATLOG_OCR_RECEIVED_ONLY=true
```

If Chatlog and the OCR stack run on different machines, bind the SDK server to
a private interface and set `CHATLOG_OCR_ENDPOINT` to that private address.
The same settings can be changed live from the dedicated Web `OCR 识别` page,
including realtime/history switches and selected contacts/chatrooms.

## Stop

```bash
docker compose -f docker-compose.vllm.yml down
```

Model caches are retained in the `glmocr-huggingface` volume.
