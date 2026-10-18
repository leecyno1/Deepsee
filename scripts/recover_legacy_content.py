"""Recover missing local content into a bundled runtime, without replacing live data.

Dry-run by default. Historical mail accounts are restored disabled and without
credentials; this never reconnects an old mailbox or changes WeChat settings.
"""
from __future__ import annotations

import argparse
import json
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path


def recover_content(source: Path, runtime: Path, *, apply: bool = False, mp_db: Path | None = None) -> dict:
    source, runtime = source.resolve(), runtime.resolve()
    if source == runtime:
        raise ValueError("Source and runtime must differ")
    legacy = source / "data/app.db"
    current = runtime / "data/app.db"
    with sqlite3.connect(f"{current.as_uri()}?mode=rw", uri=True, timeout=30) as db:
        db.execute("ATTACH DATABASE ? AS legacy", (f"{legacy.as_uri()}?mode=ro",))
        old_mail = db.execute("SELECT count(*) FROM legacy.email_messages").fetchone()[0]
        new_mail = db.execute("SELECT count(*) FROM main.email_messages").fetchone()[0]
        new_accounts = db.execute("SELECT count(*) FROM main.email_accounts").fetchone()[0]
        import_mail = old_mail > 0 and new_mail == 0
        if import_mail and new_accounts:
            raise ValueError("Runtime already has mail accounts; explicit account mapping is required")
        files = []
        for folder in ("minutes", "recordings"):
            for path in (source / "data" / folder).rglob("*"):
                target = runtime / "data" / path.relative_to(source / "data")
                if path.is_file() and not path.is_symlink() and not target.exists():
                    files.append((path, target))
        row = db.execute("SELECT value FROM sync_state WHERE key='mp_config'").fetchone()
        config = json.loads(row[0]) if row and row[0] else {}
        set_mp = bool(mp_db and mp_db.is_file() and not config.get("db_path"))
        report = {"apply": apply, "emails_to_restore": old_mail if import_mail else 0,
                  "existing_emails": new_mail, "files_to_restore": len(files), "connect_local_mp": set_mp}
        if not apply or not (import_mail or files or set_mp):
            return report
        backup = runtime / "data/backups" / f"before-content-recovery-{datetime.now():%Y%m%d-%H%M%S-%f}.db"
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as saved:
            db.backup(saved, name="main")
        report["backup"] = str(backup)
        with db:
            if import_mail:
                for table in ("email_accounts", "email_messages"):
                    cols = [r[1] for r in db.execute(f"PRAGMA main.table_info({table})")]
                    expressions = []
                    for col in cols:
                        if table == "email_accounts" and col == "auth":
                            expressions.append("'{}'")
                        elif table == "email_accounts" and col == "enabled":
                            expressions.append("0")
                        elif col == "derived":
                            expressions.append("CASE WHEN json_valid(derived) THEN CASE WHEN json_type(derived)='object' THEN derived END END")
                        else:
                            expressions.append(f'"{col}"')
                    names = ",".join(f'"{c}"' for c in cols)
                    db.execute(f'INSERT INTO main.{table} ({names}) SELECT {",".join(expressions)} FROM legacy.{table}')
            if set_mp:
                config["db_path"] = str(mp_db.resolve())
                db.execute("INSERT INTO sync_state(key,value,updated_at) VALUES('mp_config',?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at",
                           (json.dumps(config, ensure_ascii=False),))
        for src, dst in files:
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)
        return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--mp-db", type=Path)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    print(json.dumps(recover_content(args.source, args.runtime, apply=args.apply, mp_db=args.mp_db), ensure_ascii=False, indent=2))
