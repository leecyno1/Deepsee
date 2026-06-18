from pydantic_settings import BaseSettings, SettingsConfigDict
from pydantic import AnyUrl, Field


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    APP_ENV: str = "development"
    CORS_ALLOW_ORIGINS: str | None = None

    # chatlog
    CHATLOG_HTTP_BASE: str = Field(default="http://127.0.0.1:5030")
    CHATLOG_DIR: str | None = None
    CHATLOG_HTTP_SESSION_TIMEOUT_SECONDS: int = Field(default=5)
    CHATLOG_HTTP_TIMEOUT_SECONDS: int = Field(default=10)

    # n8n webhooks
    N8N_REPLY_WEBHOOK: str | None = None
    N8N_SUMMARY_WEBHOOK: str | None = None
    N8N_CONTACT_WEBHOOK: str | None = None
    N8N_SEND_WEBHOOK: str | None = None
    N8N_AUTH_TOKEN: str | None = None

    # API
    API_TOKEN: str | None = None
    AGENT_API_TOKEN: str | None = None
    AGENT_API_TOKENS: str | None = None
    AGENT_API_ALLOWLIST: str | None = None
    AGENT_API_BLOCKLIST: str | None = None

    # DB
    DATABASE_URL: str = Field(default="sqlite:///./data/app.db")

    # Server
    HOST: str = Field(default="127.0.0.1")
    PORT: int = Field(default=8001)
    SYNC_INTERVAL_SECONDS: int | None = Field(default=0)
    EMAIL_SYNC_INTERVAL_SECONDS: int | None = Field(default=0)
    SUMMARY_OVERLAY_INTERVAL_SECONDS: int | None = Field(default=3600)

    # LLM
    SILICONFLOW_API_KEY: str | None = None
    SILICONFLOW_API_URL: str | None = "https://api.siliconflow.cn/v1"
    SILICONFLOW_MODEL: str | None = "Qwen/Qwen3-30B-A3B"
    SILICONFLOW_TOOL_MODEL: str | None = "Qwen/Qwen3-8B"
    AI_MAX_PARALLEL: int = 3

    # Market data
    TUSHARE_TOKEN: str | None = None

    # WeChatPadPro
    WECHATPAD_HTTP_BASE: str | None = None  # e.g., http://60.205.58.39:1238
    WECHATPAD_TEXT_PATH: str | None = "/api/v1/message/sendText"  # fallback path for text sending

    # Extensions / Adapters
    LANGBOT_ADAPTER_LOG_DIR: str | None = None  # e.g., ./data/adapters

    MS_TENANT: str | None = "consumers"  # common/organizations/consumers

    # NewsNow aggregation (server on :4445)
    NEWSNOW_ENABLED: bool = True
    NEWSNOW_API_BASE: str = Field(default="http://localhost:4445")
    NEWSNOW_CACHE_TTL: int = Field(default=300)  # seconds
    # 默认每小时刷新一次（可用 .env 覆盖）
    NEWSNOW_REFRESH_INTERVAL_SECONDS: int | None = Field(default=3600)  # 0 = disabled (manual only)
    # 每3小时写入一次新闻舆情底层快照（datasets JSON）
    NEWS_SNAPSHOT_INTERVAL_SECONDS: int | None = Field(default=10800)
    AGGREGATION_RETENTION_DAYS: int = Field(default=90)
    AGGREGATION_RETENTION_INTERVAL_SECONDS: int = Field(default=86400)

    # Optional: MediaCrawlerPro server base (meeting recorder controls proxy)
    MEDIA_SERVER_BASE: str | None = None
    MEDIA_COLLECTOR_DAILY_ENABLED: bool = True
    MEDIA_COLLECTOR_DAILY_HOUR: int = Field(default=5)
    MEDIA_COLLECTOR_DAILY_MINUTE: int = Field(default=0)
    MEDIA_COLLECTOR_TIMEOUT_SECONDS: int = Field(default=240)

settings = Settings()
