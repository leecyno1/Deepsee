import asyncio
import json
import os
import sys
from pathlib import Path


def _normalize_target(raw: str) -> tuple[str, str]:
    raw = str(raw or "").strip()
    if not raw:
        return "", "c2c"
    lower = raw.lower()
    prefixes = (
        ("qqbot:c2c:", "c2c"),
        ("qqbot:group:", "group"),
        ("qqbot:guild:", "guild"),
        ("qqbot:channel:", "guild"),
        ("c2c:", "c2c"),
        ("group:", "group"),
        ("guild:", "guild"),
        ("channel:", "guild"),
    )
    for prefix, chat_type in prefixes:
        if lower.startswith(prefix):
            return raw[len(prefix):].strip(), chat_type
    return raw, "c2c"


def main() -> int:
    payload = json.load(sys.stdin)
    hermes_root = str(payload.get("hermes_root") or "").strip()
    if not hermes_root:
        raise RuntimeError("Hermes root missing")
    if hermes_root not in sys.path:
        sys.path.insert(0, hermes_root)

    from gateway.config import PlatformConfig
    from gateway.platforms.qqbot import QQAdapter, _ssrf_redirect_guard
    import httpx

    class OutboundQQAdapter(QQAdapter):
        """One-shot REST sender that does not own a gateway WebSocket.

        Chatlog starts this bridge only for one outbound delivery. QQ media and
        text are sent through the official REST endpoints, so requiring a
        second WebSocket connection here both duplicates the resident Hermes
        gateway and trips its single-app lock. Keep the upstream media/upload
        implementation while defining readiness by the REST client lifecycle.
        """

        @property
        def is_connected(self) -> bool:
            return bool(self._running and self._http_client is not None)

    async def run() -> dict:
        app_id = str(payload.get("app_id") or "").strip()
        client_secret = str(payload.get("client_secret") or "").strip()
        raw_chat_id = str(payload.get("chat_id") or "").strip()
        text = str(payload.get("text") or "").strip()
        media_paths = [str(p).strip() for p in (payload.get("media_paths") or []) if str(p).strip()]
        if not app_id:
            return {"success": False, "error": "QQ app_id missing"}
        if not client_secret:
            return {"success": False, "error": "QQ client_secret missing"}
        if not raw_chat_id:
            return {"success": False, "error": "QQ home channel missing"}

        chat_id, chat_type = _normalize_target(raw_chat_id)
        if not chat_id:
            return {"success": False, "error": "QQ home channel missing"}

        async with httpx.AsyncClient(
            timeout=30.0,
            follow_redirects=True,
            event_hooks={"response": [_ssrf_redirect_guard]},
        ) as client:
            adapter = OutboundQQAdapter(
                PlatformConfig(
                    enabled=True,
                    extra={
                        "app_id": app_id,
                        "client_secret": client_secret,
                    },
                )
            )
            adapter._http_client = client
            adapter._running = True
            if chat_type != "c2c":
                adapter._chat_type_map[chat_id] = chat_type

            send_text_first = text and not media_paths
            if send_text_first:
                result = await adapter.send(chat_id, text)
                if not result.success:
                    return {"success": False, "error": result.error or "qq text send failed"}

            for index, media_path in enumerate(media_paths):
                caption = text if index == 0 else None
                suffix = Path(media_path).suffix.lower()
                if suffix in {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}:
                    result = await adapter.send_image_file(chat_id, media_path, caption=caption)
                elif suffix in {".mp4", ".mov", ".m4v", ".avi", ".mkv"}:
                    result = await adapter.send_video(chat_id, media_path, caption=caption)
                elif suffix in {".silk", ".amr", ".wav", ".mp3", ".m4a", ".ogg", ".aac", ".flac"}:
                    result = await adapter.send_voice(chat_id, media_path, caption=caption)
                else:
                    result = await adapter.send_document(chat_id, media_path, caption=caption)
                if not result.success:
                    return {"success": False, "error": result.error or f"qq media send failed: {media_path}"}

        return {"success": True}

    try:
        result = asyncio.run(run())
    except Exception as exc:
        result = {"success": False, "error": str(exc)}
    json.dump(result, sys.stdout, ensure_ascii=False)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
