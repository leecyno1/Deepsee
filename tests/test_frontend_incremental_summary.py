from pathlib import Path

INDEX_HTML = Path(__file__).resolve().parents[1] / "static" / "index.html"

def test_message_overlay_uses_incremental_derive_without_force():
    html = INDEX_HTML.read_text(encoding="utf-8")
    start = html.index("async function deriveAndOverlayForLoadedRows")
    block = html[start: html.index("// 等待完成", start)]
    assert "message_ids: ids, force: false" in block
    assert "message_ids: ids, force: true" not in block


def test_wechat_auto_derive_threshold_matches_backend():
    html = INDEX_HTML.read_text(encoding="utf-8")
    assert "const AI_MIN_CHARS = 20" in html
    assert "const AI_MIN_CHARS = 50" not in html
