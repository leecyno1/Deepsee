# 深瞳 DeepPupil — Deployment Guide

## Quick Start (New Server)

```bash
# 1. Clone repo
git clone <your-repo-url> /opt/0913
cd /opt/0913

# 2. One-click deploy
bash scripts/deploy-0913.sh

# Follow the prompts to enter:
#   - wechatapi token + app_id
#   - Callback public URL
#   - SiliconFlow API key
#   - LLM API key
#   - API token
```

After deploy, set up a public tunnel (ngrok/natapp/frp) to expose port 8001,
then visit the configured service URL, for example http://127.0.0.1:8001 or your reverse-proxy domain.

## What Gets Deployed

| Component | Description |
|-----------|-------------|
| FastAPI server | Port 8001 — message ingestion, auto-reply, analysis dashboard |
| SQLite DB | data/app.db — messages, contacts, config, subsession state |
| WeChat gateway | Callback → trigger rules → reply generation → outbound send |
| DeepPupil Dashboard | 信息流看板、微信/邮件/新闻/自媒体/公众号/会议引擎、AI分析与群发管理 |
| Sub-session | wechat_gateway_default — independent persona, MiniMax routing, multi-turn history |

## Configuration Files

| File | Purpose |
|------|---------|
| `.env` | Server settings, API keys, paths |
| `data/ai_config.json` | LLM model routes, channels, prompts |
| SyncState `wechat_gateway_config` | Gateway config (token, app_id, callback URL) |
| SyncState `wechat_gateway_trigger_rules` | Auto-reply trigger rules (prefix, wakeup, suppression) |
| wechat_subsessions table | Sub-session persona, routing, history |

## Post-Deploy Checklist

- [ ] `curl http://127.0.0.1:8001/api/health` returns 200
- [ ] `curl http://127.0.0.1:8001/api/ready` returns readiness checks
- [ ] If exposed beyond localhost, `API_TOKEN` is set and reverse proxy forwards `Authorization` headers
- [ ] Public tunnel set up to forward to port 8001
- [ ] 8001 → WeChat Settings → verify callback URL → Bind Callback
- [ ] Send `ai test` from WeChat → verify auto-reply
- [ ] 8001 → Message list shows WeChat traffic
- [ ] 8001 → 公众号 tab loads articles

## Directory Structure

```
/opt/0913/
├── app/                 # FastAPI app (routers, services, models)
├── static/              # 8001 dashboard frontend
├── data/                # SQLite DB, ai_config.json
├── scripts/             # manage.sh, deploy-0913.sh
├── tests/               # pytest test suite
├── docs/                # WeChat API docs mirror (142 pages)
├── .env                 # Server config (from template)
├── .env.example         # Config template
├── requirements.txt     # Python dependencies
└── DEPLOY.md            # This file
```


## Hermes / 龙虾(OpenClaw) Integration

DeepPupil exposes an agent bridge under `/api/agent` for cloud-side assistants such as Hermes and 龙虾/OpenClaw.

Recommended production settings:

```env
AGENT_API_TOKEN=<strong-random-token>
AGENT_API_ALLOWLIST=/api/health,/api/ready,/api/messages,/api/email,/api/newsfeed,/api/ai,/api/send,/api/wechat-gateway,/api/config
AGENT_API_BLOCKLIST=/api/admin/cleanup,/api/admin/aggregation-retention/prune
HERMES_HOME=/opt/hermes
OPENCLAW_HOME=/opt/openclaw
```

Use `/api/admin/diagnostics` after login/API-token configuration to confirm database, background tasks, Chatlog, LLM routing, Hermes and OpenClaw availability.

## Adding Hermes Skill (Optional)

To give Hermes deep knowledge of the 0913 system on a new server:

```bash
# Copy the skill from your existing Hermes setup
cp -r ~/.hermes/skills/software-development/0913-wechat-smart-reply \
     /target/.hermes/skills/software-development/
```

Or package and transfer:
```bash
tar czf 0913-skill.tar.gz -C ~/.hermes/skills/software-development 0913-wechat-smart-reply
```

## Docker (100-server Scale)

```dockerfile
FROM python:3.11-slim
COPY . /app
WORKDIR /app
RUN pip install -r requirements.txt
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8001"]
```

Environment variables are injected per instance — no code changes needed.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| No messages | curl /api/health; check wechatapi checkOnline; verify token |
| No auto-reply | check trigger_rules; verify LLM API key; check logs |
| 公众号 empty | Check /api/mp/articles returns data; verify mp_config |
| Token expired | Update token in 8001 → WeChat Settings; re-bind callback |
