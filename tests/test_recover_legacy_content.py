import importlib.util
import json
import sqlite3
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location("recover", Path(__file__).parents[1] / "scripts/recover_legacy_content.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def stores(tmp_path):
    roots = [tmp_path / "old", tmp_path / "runtime"]
    for root in roots:
        (root / "data").mkdir(parents=True)
        with sqlite3.connect(root / "data/app.db") as db:
            db.executescript("CREATE TABLE email_accounts(id INTEGER PRIMARY KEY, auth TEXT, enabled INTEGER);"
                             "CREATE TABLE email_messages(id INTEGER PRIMARY KEY, account_id INTEGER, derived TEXT);"
                             "CREATE TABLE sync_state(key TEXT PRIMARY KEY, value TEXT, updated_at TEXT NOT NULL);")
    with sqlite3.connect(roots[0] / "data/app.db") as db:
        db.execute("INSERT INTO email_accounts VALUES(1,?,1)", ('{"password":"test-only"}',))
        db.executemany("INSERT INTO email_messages VALUES(?,1,?)", [(1, '{"summary":"历史"}'), (2, 'legacy plain text')])
    (roots[0] / "data/recordings").mkdir()
    (roots[0] / "data/recordings/old.txt").write_text("historical transcript")
    return roots


def test_dry_run_does_not_change_live_store(tmp_path):
    old, runtime = stores(tmp_path)
    report = module.recover_content(old, runtime)
    assert report["emails_to_restore"] == 2
    assert not (runtime / "data/recordings").exists()
    with sqlite3.connect(runtime / "data/app.db") as db:
        assert db.execute("SELECT count(*) FROM email_messages").fetchone()[0] == 0


def test_recovery_is_additive_and_does_not_activate_mail_accounts(tmp_path):
    old, runtime = stores(tmp_path)
    mp = tmp_path / "mp.db"
    mp.touch()
    result = module.recover_content(old, runtime, apply=True, mp_db=mp)
    assert Path(result["backup"]).is_file()
    with sqlite3.connect(runtime / "data/app.db") as db:
        assert db.execute("SELECT auth,enabled FROM email_accounts").fetchone() == ("{}", 0)
        assert db.execute("SELECT derived FROM email_messages WHERE id=2").fetchone()[0] is None
        assert json.loads(db.execute("SELECT value FROM sync_state").fetchone()[0])["db_path"] == str(mp)
    assert (runtime / "data/recordings/old.txt").read_text() == "historical transcript"
    again = module.recover_content(old, runtime, apply=True, mp_db=mp)
    assert again["emails_to_restore"] == again["files_to_restore"] == 0
    assert "backup" not in again


def test_existing_accounts_require_explicit_mapping(tmp_path):
    old, runtime = stores(tmp_path)
    with sqlite3.connect(runtime / "data/app.db") as db:
        db.execute("INSERT INTO email_accounts VALUES(1,'{}',1)")
    with pytest.raises(ValueError, match="account mapping"):
        module.recover_content(old, runtime, apply=True)
