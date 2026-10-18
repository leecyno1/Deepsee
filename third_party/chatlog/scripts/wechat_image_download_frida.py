#!/usr/bin/env python3
"""Upgrade incoming WeChat macOS image requests via a resident Frida hook.

Live-verified passive hook offsets (WeChat 4.1.12 / 269341):
  StartC2CDownload_A hook     @ 0x53445dc
  StartC2CDownload_B hook     @ 0x5353f98
  _startDownloadMedia hook    @ 0x5398630
  CreateC2C task hook         @ 0x5501978

Modes:
  oneshot (default): attach → optional prime wait → download → detach → exit
  --daemon: long-lived attach, Unix socket JSON-lines IPC for many downloads

The default daemon mutates matching live incoming requests from thumbnail (3)
to normal image (2). It also supports a bounded replay for an already indexed
message by constructing the minimal StartC2CDownload request on WeChat's main
queue; no stale pointers from an earlier captured request are reused.
"""
from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
import signal
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any, Optional

RESOURCE = Path("/Applications/WeChat.app/Contents/Resources/wechat.dylib")
EXPECTED_SHA = "f28329ed2599e8567f6b0a09f031d05666a48abd97dbc7a3380891ddbcff6cdc"
DEFAULT_SOCKET = os.path.join(
    os.environ.get("TMPDIR") or "/tmp",
    "chatlog-wechat-imgdl.sock",
)

AGENT = r"""
'use strict';
const RESOURCE_SUFFIX = '/Contents/Resources/wechat.dylib';
const OFF = {
  create: 0x5501948,
  startA: 0x53445d0,
  startB: 0x5353f84,
  startMedia: 0x539861c,
};

let captured = {
  manager: null,
  req: null,
  a3: null,
  a7: null,
  hits: 0,
  pathHits: { create: 0, startMedia: 0, startB: 0, startA: 0 },
  lastPath: null,
  managerPath: null,
  managerAt: 0,
  samples: [],
  requestFields: {},
  template: null,
  templatePins: [],
};
let traceRequests = false;
let upgradeReceive = false;
let scopedReceive = false;
let scopedTalkers = [];
let upgradeCount = 0;
let scopedUpgradeCount = 0;
let scopedSkipCount = 0;
let scopedTalkerMatchCount = 0;
let lastUpgradeAt = 0;
let scopedDecisionSeq = 0;
let activeRequests = [];

function readRequestFileId(req) {
  try {
    const field = req.add(0x58);
    const length = Number(field.add(8).readU64());
    if (length <= 0 || length > 4096) return '';
    const data = field.readPointer();
    if (!data || data.isNull()) return '';
    return data.readUtf8String(length) || '';
  } catch (e) {
    return '';
  }
}

function readRequestScopeKey(req) {
  // The receive task stores `<talker>_<timestamp>_<local-id>_<variant>` in
  // the first std::string field.  Unlike CDN metadata, this is available
  // before the message database change stream publishes the new row.
  return readStdString(req, 0x40);
}

function readStdString(obj, offset) {
  try {
    const field = obj.add(offset);
    const marker = field.add(0x17).readS8();
    let length = marker;
    let data = field;
    if (marker < 0) {
      length = Number(field.add(8).readU64());
      data = field.readPointer();
    }
    if (length <= 0 || length > 4096 || !data || data.isNull()) return '';
    return data.readUtf8String(length) || '';
  } catch (e) {
    return '';
  }
}

function snapshotRequestFields(req) {
  const out = {};
  for (let offset = 0x40; offset <= 0x448; offset += 0x18) {
    const value = readStdString(req, offset);
    if (!value) continue;
    let printable = true;
    for (let i = 0; i < value.length; i++) {
      const code = value.charCodeAt(i);
      if (code < 0x20 || code > 0x7e) {
        printable = false;
        break;
      }
    }
    if (printable) out['0x' + offset.toString(16)] = value;
  }
  return out;
}

function captureRequestTemplate(req) {
  try {
    const template = Memory.alloc(0x480);
    Memory.copy(template, req, 0x480);
    const pins = [];
    const fields = snapshotRequestFields(req);
    Object.keys(fields).forEach(function (key) {
      writeStdString(template, parseInt(key, 16), fields[key], pins);
    });
    captured.template = template;
    captured.templatePins = pins;
    captured.requestFields = fields;
  } catch (e) {
    emit('warn', { message: 'template capture failed', error: String(e) });
  }
}

function scopedUpgradeAllowed(req) {
  const fileId = readRequestFileId(req);
  const scopeKey = readRequestScopeKey(req);
  if (!fileId && !scopeKey) return false;
  for (let i = 0; i < scopedTalkers.length; i++) {
    const talker = scopedTalkers[i];
    if (scopeKey === talker || scopeKey.indexOf(talker + '_') === 0) {
      scopedTalkerMatchCount += 1;
      return true;
    }
  }
  scopedDecisionSeq += 1;
  const responseType = 'scoped_upgrade_decision_' + scopedDecisionSeq;
  let allowed = false;
  const response = recv(responseType, function (message) {
    const payload = (message && message.payload) || {};
    allowed = payload.allow === true;
  });
  send({
    type: 'scoped_upgrade_candidate',
    response_type: responseType,
    file_id: fileId,
    scope_key: scopeKey,
  });
  response.wait();
  return allowed;
}

function hexBytes(address, size) {
  const bytes = new Uint8Array(address.readByteArray(size));
  let out = '';
  for (let i = 0; i < bytes.length; i++) {
    out += ('0' + bytes[i].toString(16)).slice(-2);
  }
  return out;
}

function emit(type, extra) {
  send(Object.assign({ type: type, ts: Date.now() }, extra || {}));
}

function mod() {
  const ms = Process.enumerateModules().filter(m => m.path.endsWith(RESOURCE_SUFFIX));
  if (ms.length !== 1) throw new Error('wechat.dylib count=' + ms.length);
  return ms[0];
}

function installCapture() {
  const m = mod();
  const targets = [
    { name: 'create', off: OFF.create },
    { name: 'startMedia', off: OFF.startMedia },
    { name: 'startB', off: OFF.startB },
    { name: 'startA', off: OFF.startA },
  ];
  targets.forEach(function (t) {
    Interceptor.attach(m.base.add(t.off), {
      onEnter: function (args) {
        try {
          captured.hits += 1;
          captured.pathHits[t.name] = (captured.pathHits[t.name] || 0) + 1;
          captured.lastPath = t.name;
          if (t.name === 'startA') {
            if (args[0] && !args[0].isNull()) {
              captured.req = args[0];
              captureRequestTemplate(args[0]);
              const variantBefore = args[0].add(0xa0).readS32();
              if (traceRequests) {
                captured.samples.push({
                  ts: Date.now(),
                  path: t.name,
                  req: args[0].toString(),
                  variant: variantBefore,
                  hex: hexBytes(args[0], 0x480),
                });
                if (captured.samples.length > 12) captured.samples.shift();
              }
              // Incoming thumbnail tasks use variant 3; opening the same image
              // naturally uses variant 2 and lands the normal .dat payload.
              // Mutate the live request before WeChat processes it instead of
              // invoking a private routine later from Frida's JS thread.
              if (variantBefore === 3) {
                let shouldUpgrade = upgradeReceive;
                if (!shouldUpgrade && scopedReceive) {
                  shouldUpgrade = scopedUpgradeAllowed(args[0]);
                  if (shouldUpgrade) scopedUpgradeCount += 1;
                  else scopedSkipCount += 1;
                }
                if (shouldUpgrade) {
                  args[0].add(0xa0).writeS32(2);
                  upgradeCount += 1;
                  lastUpgradeAt = Date.now();
                }
              }
            }
            return;
          }
          if (t.name === 'startB') {
            if (args[0] && !args[0].isNull()) {
              captured.manager = args[0];
              captured.managerPath = t.name;
              captured.managerAt = Date.now();
            }
            if (args[1] && !args[1].isNull()) captured.req = args[1];
            return;
          }
          if (args[0] && !args[0].isNull()) {
            captured.manager = args[0];
            captured.managerPath = t.name;
            captured.managerAt = Date.now();
          }
          if (args[1] && !args[1].isNull()) captured.req = args[1];
          if (args[3] && !args[3].isNull()) captured.a3 = args[3];
          if (args[7] && !args[7].isNull()) captured.a7 = args[7];
        } catch (e) {
          emit('warn', { message: 'capture failed', error: String(e) });
        }
      }
    });
  });
  emit('capture_ready', { base: m.base.toString() });
}

// libc++ std::string (ARM64 Apple): long form
//   +0x00 data*
//   +0x08 size
//   +0x10 capacity with the high bit set
function writeStdString(obj, offset, text, pins) {
  const s = String(text || '');
  const data = Memory.allocUtf8String(s);
  pins.push(data);
  const base = obj.add(offset);
  base.writePointer(data);
  base.add(8).writeU64(uint64(s.length));
  const cap = s.length + 8;
  base.add(16).writeU64(uint64('0x8000000000000000').add(cap));
  return data;
}

function buildRequest(fileId, aesKey, requestKey, mediaKey, mediaPath, mediaLength) {
  const pins = [];
  const req = Memory.alloc(0x480);
  for (let i = 0; i < 0x480; i += 8) req.add(i).writeU64(uint64(0));

  // StartC2CDownload checks the first word as a presence flag.  Earlier
  // experiments copied an old request and replaced this word with a pointer;
  // the remaining shallow pointers later expired and crashed WeChat.  Build a
  // self-contained request instead and pin every string for the task lifetime.
  req.writeU64(uint64(1));
  writeStdString(req, 0x40, requestKey || mediaKey || fileId, pins);
  writeStdString(req, 0x58, fileId, pins);
  writeStdString(req, 0x70, aesKey, pins);
  writeStdString(req, 0x88, mediaPath || mediaKey || fileId, pins);
  req.add(0xa0).writeS32(2);
  const parsedLength = parseInt(String(mediaLength || '0'), 10);
  if (Number.isFinite(parsedLength) && parsedLength > 0) {
    req.add(0x108).writeU64(uint64(parsedLength));
  }
  activeRequests = activeRequests.filter(function (item) {
    return Date.now() - item.at < 120000;
  });
  activeRequests.push({ req: req, pins: pins, at: Date.now() });
  if (activeRequests.length > 32) activeRequests.shift();
  return req;
}

function callStartA(req) {
  const m = mod();
  const fn = new NativeFunction(m.base.add(OFF.startA), 'int', ['pointer']);
  const beforeHits = captured.hits;
  const rv = fn(req);
  const ok = rv >= 0;
  return {
    ok: ok,
    mode: 'startA',
    rv: rv,
    error: ok ? '' : ('startA_returned_' + rv),
    hits_delta: captured.hits - beforeHits,
    manager_after: !!(captured.manager && !captured.manager.isNull()),
    last_path: captured.lastPath,
  };
}

function callStartAOnMain(req) {
  return new Promise(function (resolve) {
    const run = function () {
      try {
        resolve(callStartA(req));
      } catch (e) {
        resolve({ ok: false, error: 'startA_call_failed:' + String(e) });
      }
    };
    if (typeof ObjC !== 'undefined' && ObjC.available) {
      ObjC.schedule(ObjC.mainQueue, run);
    } else {
      run();
    }
  });
}

function callStartMedia(req) {
  const m = mod();
  const manager = captured.manager;
  if (!manager || manager.isNull()) {
    return { ok: false, error: 'no_manager_for_start_media' };
  }
  let inner = ptr(0);
  try {
    inner = manager.add(0x40).readPointer();
  } catch (e) {
    return { ok: false, error: 'manager_inner_read_failed:' + String(e) };
  }
  if (!inner || inner.isNull()) {
    return { ok: false, error: 'no_manager_inner_for_start_media' };
  }
  const fn = new NativeFunction(m.base.add(OFF.startMedia), 'int', ['pointer', 'pointer']);
  const beforeHits = captured.hits;
  const rv = fn(inner, req);
  return {
    ok: rv === 0,
    mode: 'startMedia',
    rv: rv,
    hits_delta: captured.hits - beforeHits,
    manager: manager.toString(),
    manager_inner: inner.toString(),
  };
}

function callCreate(req) {
  const m = mod();
  const manager = captured.manager;
  if (!manager || manager.isNull()) {
    return { ok: false, error: 'no_manager_for_create' };
  }
  const a3 = captured.a3 || ptr(0);
  const a7 = captured.a7 || ptr(0);
  const fn = new NativeFunction(
    m.base.add(OFF.create),
    'pointer',
    ['pointer', 'pointer', 'int', 'pointer', 'int', 'int', 'int', 'pointer']
  );
  const rv = fn(manager, req, 1, a3, 80, 12, 0, a7);
  return {
    ok: true,
    mode: 'create',
    rv: rv ? rv.toString() : 'null',
    manager: manager.toString(),
  };
}

function invokeDownload(fileId, aesKey, requestKey, mediaKey, mediaPath, mediaLength, mode) {
  const req = buildRequest(fileId, aesKey, requestKey, mediaKey, mediaPath, mediaLength);
  const hasManager = !!(captured.manager && !captured.manager.isNull());

  emit('invoke_begin', {
    mode: mode,
    has_manager: hasManager,
    has_template: !!(captured.template && !captured.template.isNull()),
    capture_hits: captured.hits,
    file_id_len: String(fileId || '').length,
    aes_key_len: String(aesKey || '').length,
    request_key: String(requestKey || ''),
    media_length: String(mediaLength || ''),
  });

  if (mode === 'create') {
    return { ok: false, error: 'create_mode_disabled' };
  }

  if (mode === 'auto') {
    return callStartAOnMain(req);
  }

  if (mode === 'startA') {
    return callStartAOnMain(req);
  }

  return { ok: false, error: 'no_invoke_path' };
}

rpc.exports = {
  prepare: function () {
    installCapture();
    return { ok: true, hits: captured.hits };
  },
  status: function () {
    const out = {
      hits: captured.hits,
      has_manager: !!(captured.manager && !captured.manager.isNull()),
      has_req: !!(captured.req && !captured.req.isNull()),
      last_path: captured.lastPath,
      path_hits: captured.pathHits,
      manager_path: captured.managerPath,
      manager_at: captured.managerAt || 0,
    };
    if (traceRequests) out.samples = captured.samples;
    out.upgrade_receive = upgradeReceive;
    out.scoped_receive = scopedReceive;
    out.upgrade_count = upgradeCount;
    out.scoped_upgrade_count = scopedUpgradeCount;
    out.scoped_skip_count = scopedSkipCount;
    out.scope_talker_count = scopedTalkers.length;
    out.scope_talker_match_count = scopedTalkerMatchCount;
    out.last_upgrade_at = lastUpgradeAt;
    out.request_fields = captured.requestFields;
    return out;
  },
  settrace: function (enabled) {
    traceRequests = !!enabled;
    if (!traceRequests) captured.samples = [];
    return { ok: true, trace_requests: traceRequests };
  },
  setupgrade: function (enabled) {
    upgradeReceive = !!enabled;
    return { ok: true, upgrade_receive: upgradeReceive };
  },
  setscoped: function (enabled) {
    scopedReceive = !!enabled;
    return { ok: true, scoped_receive: scopedReceive };
  },
  setscopetalkers: function (talkers) {
    const seen = {};
    scopedTalkers = [];
    (talkers || []).forEach(function (value) {
      const talker = String(value || '').trim();
      if (!talker || seen[talker]) return;
      seen[talker] = true;
      scopedTalkers.push(talker);
    });
    return { ok: true, scope_talker_count: scopedTalkers.length };
  },
  download: function (fileId, aesKey, requestKey, mediaKey, mediaPath, mediaLength, mode) {
    return invokeDownload(
      String(fileId || ''),
      String(aesKey || ''),
      String(requestKey || ''),
      String(mediaKey || ''),
      String(mediaPath || ''),
      String(mediaLength || ''),
      mode || 'auto'
    );
  },
};
emit('agent_loaded', { message: 'image_download_invoke ready' });
"""


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def find_wechat_pid() -> int:
    lines = subprocess.check_output(["pgrep", "-x", "WeChat"], text=True).strip().splitlines()
    pids = [int(x) for x in lines if x.strip().isdigit()]
    if not pids:
        raise RuntimeError("WeChat not running")
    return sorted(pids)[0]


def process_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def wechat_alive(pid: int) -> bool:
    return process_alive(pid)


def run_with_timeout(name: str, fn, timeout: float):
    box: dict[str, Any] = {}
    done = threading.Event()

    def worker() -> None:
        try:
            box["result"] = fn()
        except Exception as exc:  # noqa: BLE001
            box["error"] = exc
        finally:
            done.set()

    threading.Thread(target=worker, name=f"{name}-timeout", daemon=True).start()
    if not done.wait(timeout):
        raise TimeoutError(f"{name} timed out after {timeout:.1f}s")
    if "error" in box:
        raise box["error"]
    return box.get("result")


def emit_result_and_exit(result: dict[str, Any], code: int) -> None:
    try:
        print(json.dumps(result, ensure_ascii=False), flush=True)
    except Exception:
        print('{"ok":false,"error":"failed_to_serialize_result"}', flush=True)
        code = 2
    os._exit(code)


def poll_path(path: Path, min_size: int, timeout: float) -> dict:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if path.exists():
            try:
                size = path.stat().st_size
            except OSError:
                size = 0
            if size >= min_size:
                return {"ready": True, "path": str(path), "size": size}
        time.sleep(0.4)
    exists = path.exists()
    size = path.stat().st_size if exists else 0
    return {"ready": False, "path": str(path), "size": size, "exists": exists}


class FridaSession:
    """Long-lived Frida attach to WeChat with CDN capture hooks."""

    def __init__(
        self,
        *,
        allow_hash_mismatch: bool = False,
        attach_timeout: float = 8.0,
        invoke_timeout: float = 8.0,
        trace_requests: bool = False,
        upgrade_receive: bool = False,
        scoped_receive: bool = False,
    ) -> None:
        self.allow_hash_mismatch = allow_hash_mismatch
        self.attach_timeout = attach_timeout
        self.invoke_timeout = invoke_timeout
        self.trace_requests = trace_requests
        self.upgrade_receive = upgrade_receive
        self.scoped_receive = scoped_receive
        self._lock = threading.RLock()
        self._frida = None
        self._device = None
        self._session = None
        self._script = None
        self._pid: Optional[int] = None
        self._events: list[dict] = []
        self._resource_sha = ""
        self._attached_at = 0.0
        self._scope_condition = threading.Condition()
        self._scope_file_ids: dict[str, float] = {}
        self._scope_talkers: set[str] = set()
        self._scope_decision_wait = 0.9
        self._scope_decisions = 0
        self._scope_talker_matches = 0
        self._scope_file_matches = 0
        self._scope_misses = 0
        self._scope_last_key = ""
        self._scope_last_file_id = ""
        self._scope_last_reason = ""

    def _on_message(self, message, _data):  # noqa: ANN001
        if message.get("type") == "send":
            payload = message.get("payload") or {}
            if isinstance(payload, dict):
                if payload.get("type") == "scoped_upgrade_candidate":
                    threading.Thread(
                        target=self._respond_scoped_upgrade,
                        args=(payload,),
                        name="imgdl-scope-decision",
                        daemon=True,
                    ).start()
                    return
                self._events.append(payload)
                if len(self._events) > 200:
                    self._events = self._events[-100:]
        elif message.get("type") == "error":
            self._events.append(
                {"type": "frida_error", "description": message.get("description")}
            )

    def _respond_scoped_upgrade(self, payload: dict[str, Any]) -> None:
        file_id = str(payload.get("file_id") or "")
        scope_key = str(payload.get("scope_key") or "")
        response_type = str(payload.get("response_type") or "")
        deadline = time.monotonic() + self._scope_decision_wait
        allowed = False
        reason = "miss"
        with self._scope_condition:
            self._scope_decisions += 1
            self._scope_last_key = scope_key
            self._scope_last_file_id = file_id
            while True:
                now = time.time()
                expired = [key for key, expires in self._scope_file_ids.items() if expires <= now]
                for key in expired:
                    self._scope_file_ids.pop(key, None)
                if self._scope_key_allowed(scope_key):
                    allowed = True
                    reason = "talker"
                    self._scope_talker_matches += 1
                    break
                if file_id and self._scope_file_ids.get(file_id, 0) > now:
                    allowed = True
                    reason = "file_id"
                    self._scope_file_matches += 1
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    break
                self._scope_condition.wait(remaining)
            if not allowed:
                self._scope_misses += 1
            self._scope_last_reason = reason
        if not response_type:
            return
        # Do not take the session RPC lock here: the intercepted WeChat thread
        # is blocked in recv().wait(), and a concurrent status RPC may itself be
        # waiting for the same JS runtime. script.post is the response channel.
        script = self._script
        if script is None:
            return
        try:
            script.post({"type": response_type, "payload": {"allow": allowed}})
        except Exception:
            pass

    def _scope_key_allowed(self, scope_key: str) -> bool:
        key = str(scope_key or "").strip()
        if not key:
            return False
        return any(key == talker or key.startswith(talker + "_") for talker in self._scope_talkers)

    def set_scoped_talkers(self, talkers: list[str]) -> dict[str, Any]:
        normalized = {str(value).strip() for value in talkers if str(value).strip()}
        with self._scope_condition:
            self._scope_talkers = normalized
            self._scope_condition.notify_all()
        with self._lock:
            if self._script is not None and self._pid and wechat_alive(self._pid):
                self._script.exports_sync.setscopetalkers(sorted(normalized))
        return {"ok": True, "scope_talker_count": len(normalized)}

    def _annotate_scope_status(self, capture: dict[str, Any]) -> dict[str, Any]:
        with self._scope_condition:
            capture["scope_talker_count"] = len(self._scope_talkers)
            capture["scope_file_count"] = len(self._scope_file_ids)
            capture["scope_decisions"] = self._scope_decisions
            capture["scope_talker_matches"] = self._scope_talker_matches
            capture["scope_file_matches"] = self._scope_file_matches
            capture["scope_misses"] = self._scope_misses
            capture["scope_last_key"] = self._scope_last_key
            capture["scope_last_file_id"] = self._scope_last_file_id
            capture["scope_last_reason"] = self._scope_last_reason
        return capture

    def allow_scoped_file_ids(self, file_ids: list[str], ttl_seconds: float) -> dict[str, Any]:
        ttl_seconds = max(5.0, min(float(ttl_seconds or 120.0), 600.0))
        expires = time.time() + ttl_seconds
        normalized = {str(value).strip() for value in file_ids if str(value).strip()}
        with self._scope_condition:
            for file_id in normalized:
                self._scope_file_ids[file_id] = expires
            self._scope_condition.notify_all()
            count = len(self._scope_file_ids)
        return {"ok": True, "registered": len(normalized), "allowlist_size": count}

    def _import_frida(self):
        if self._frida is None:
            import frida  # type: ignore

            self._frida = frida
        return self._frida

    def _check_hash(self) -> None:
        self._resource_sha = sha256_file(RESOURCE)
        if self._resource_sha != EXPECTED_SHA and not self.allow_hash_mismatch:
            raise RuntimeError(
                f"wechat.dylib hash mismatch: {self._resource_sha} (expected {EXPECTED_SHA})"
            )

    def _cleanup_unlocked(self) -> None:
        script, session, frida = self._script, self._session, self._frida
        self._script = None
        self._session = None
        self._pid = None

        def cleanup() -> None:
            if script is not None:
                try:
                    script.unload()
                except Exception:
                    pass
            if session is not None:
                try:
                    session.detach()
                except Exception:
                    pass

        try:
            run_with_timeout("frida.cleanup", cleanup, 2.0)
        except Exception:
            pass

    def ensure_attached(self, preferred_pid: int = 0) -> dict[str, Any]:
        with self._lock:
            if (
                self._script is not None
                and self._session is not None
                and self._pid
                and wechat_alive(self._pid)
            ):
                try:
                    st = self._annotate_scope_status(self._script.exports_sync.status())
                    return {
                        "ok": True,
                        "pid": self._pid,
                        "reused": True,
                        "capture": st,
                        "resource_sha256": self._resource_sha,
                        "attached_at": self._attached_at,
                    }
                except Exception:
                    self._cleanup_unlocked()

            self._cleanup_unlocked()
            self._check_hash()
            frida = self._import_frida()
            pid = preferred_pid or find_wechat_pid()
            device = frida.get_local_device()
            self._device = device

            def attach():
                return device.attach(pid)

            session = run_with_timeout("frida.attach", attach, self.attach_timeout)
            script = session.create_script(AGENT)
            script.on("message", self._on_message)
            script.load()
            script.exports_sync.prepare()
            script.exports_sync.settrace(self.trace_requests)
            script.exports_sync.setupgrade(self.upgrade_receive)
            script.exports_sync.setscoped(self.scoped_receive)
            script.exports_sync.setscopetalkers(sorted(self._scope_talkers))
            self._session = session
            self._script = script
            self._pid = pid
            self._attached_at = time.time()
            st = self._annotate_scope_status(script.exports_sync.status())
            return {
                "ok": True,
                "pid": pid,
                "reused": False,
                "capture": st,
                "resource_sha256": self._resource_sha,
                "attached_at": self._attached_at,
            }

    def status(self) -> dict[str, Any]:
        with self._lock:
            out: dict[str, Any] = {
                "ok": True,
                "attached": self._script is not None and self._pid is not None,
                "pid": self._pid,
                "resource_sha256": self._resource_sha,
                "attached_at": self._attached_at,
            }
            if self._script is not None and self._pid and wechat_alive(self._pid):
                try:
                    out["capture"] = self._annotate_scope_status(self._script.exports_sync.status())
                except Exception as exc:  # noqa: BLE001
                    out["capture_error"] = str(exc)
                    out["attached"] = False
            else:
                out["attached"] = False
                out["capture"] = {
                    "hits": 0,
                    "has_manager": False,
                    "has_req": False,
                }
            return out

    def set_upgrade_receive(self, enabled: bool) -> dict[str, Any]:
        with self._lock:
            self.upgrade_receive = bool(enabled)
            if self._script is not None and self._pid and wechat_alive(self._pid):
                self._script.exports_sync.setupgrade(self.upgrade_receive)
            return {
                "ok": True,
                "upgrade_receive": self.upgrade_receive,
                "attached": self._script is not None and self._pid is not None,
            }

    def set_scoped_receive(self, enabled: bool) -> dict[str, Any]:
        with self._lock:
            self.scoped_receive = bool(enabled)
            if self._script is not None and self._pid and wechat_alive(self._pid):
                self._script.exports_sync.setscoped(self.scoped_receive)
            return {
                "ok": True,
                "scoped_receive": self.scoped_receive,
                "attached": self._script is not None and self._pid is not None,
            }

    def download(
        self,
        file_id: str,
        aes_key: str,
        request_key: str = "",
        media_key: str = "",
        media_path: str = "",
        media_length: str = "",
        mode: str = "auto",
    ) -> dict[str, Any]:
        with self._lock:
            attach_info = self.ensure_attached()
            if not attach_info.get("ok"):
                return {"ok": False, "error": "attach_failed", "attach": attach_info}

            assert self._script is not None

            def invoke():
                return self._script.exports_sync.download(
                    file_id,
                    aes_key,
                    request_key,
                    media_key,
                    media_path,
                    media_length,
                    mode,
                )

            try:
                invoke_result = run_with_timeout(
                    "frida.download", invoke, self.invoke_timeout
                )
            except Exception as exc:  # noqa: BLE001
                # Session may be dead after a bad call; force reattach next time.
                self._cleanup_unlocked()
                return {
                    "ok": False,
                    "error": str(exc),
                    "pid": attach_info.get("pid"),
                    "resource_sha256": self._resource_sha,
                }

            capture = {}
            try:
                capture = self._script.exports_sync.status()
            except Exception:
                pass

            ok = bool(invoke_result.get("ok"))
            result: dict[str, Any] = {
                "ok": ok,
                "pid": attach_info.get("pid"),
                "resource_sha256": self._resource_sha,
                "file_id_len": len(file_id or ""),
                "aes_key_len": len(aes_key or ""),
                "capture": capture,
                "invoke": invoke_result,
                "attach_reused": bool(attach_info.get("reused")),
                "daemon": True,
            }
            if not ok and invoke_result.get("error"):
                result["error"] = invoke_result.get("error")
            result["events"] = self._events[-20:]
            return result

    def close(self) -> None:
        with self._lock:
            self._cleanup_unlocked()
            if self._frida is not None:
                try:
                    self._frida.shutdown()
                except Exception:
                    pass


def run_daemon(args: argparse.Namespace) -> int:
    sock_path = os.path.abspath(args.socket or DEFAULT_SOCKET)
    lock_path = sock_path + ".lock"
    # A Unix socket pathname can be unlinked and rebound while an older server
    # is still listening on the original inode.  That previously allowed two
    # resident Frida sessions (and two frida-helper children) to hook WeChat at
    # the same time.  Keep an advisory lock for the complete daemon lifetime so
    # only one process may own a configured socket.
    lock_file = open(lock_path, "a+")
    try:
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print(
            json.dumps(
                {
                    "ok": False,
                    "error": "image download daemon already running",
                    "socket": sock_path,
                },
                ensure_ascii=False,
            ),
            flush=True,
        )
        lock_file.close()
        return 3

    if os.path.exists(sock_path):
        # Stale socket from dead daemon?
        try:
            os.unlink(sock_path)
        except OSError:
            pass

    session = FridaSession(
        allow_hash_mismatch=bool(args.allow_hash_mismatch),
        attach_timeout=float(args.attach_timeout),
        invoke_timeout=float(args.invoke_timeout),
        trace_requests=bool(args.trace_requests),
        upgrade_receive=bool(args.upgrade_receive),
        scoped_receive=bool(args.scoped_receive),
    )

    # Best-effort initial attach (WeChat may not be running yet).
    try:
        session.ensure_attached(args.pid or 0)
        sys.stderr.write(f"[imgdl-daemon] attached pid={session._pid}\n")
    except Exception as exc:  # noqa: BLE001
        sys.stderr.write(f"[imgdl-daemon] initial attach deferred: {exc}\n")

    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(sock_path)
    try:
        os.chmod(sock_path, 0o600)
    except OSError:
        pass
    server.listen(16)
    server.settimeout(1.0)
    sys.stderr.write(f"[imgdl-daemon] listening on {sock_path}\n")
    sys.stderr.flush()

    # Ready marker for parent process.
    print(json.dumps({"ok": True, "event": "daemon_ready", "socket": sock_path}), flush=True)

    stop = threading.Event()
    parent_pid = int(args.parent_pid or os.getppid())
    args.parent_pid = parent_pid

    def handle_sig(_signum, _frame):  # noqa: ANN001
        stop.set()

    signal.signal(signal.SIGTERM, handle_sig)
    signal.signal(signal.SIGINT, handle_sig)

    def handle_client(conn: socket.socket) -> None:
        try:
            conn.settimeout(30.0)
            buf = b""
            while not stop.is_set():
                chunk = conn.recv(65536)
                if not chunk:
                    break
                buf += chunk
                while b"\n" in buf:
                    line, buf = buf.split(b"\n", 1)
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        req = json.loads(line.decode("utf-8"))
                    except Exception as exc:  # noqa: BLE001
                        resp = {"ok": False, "error": f"bad_json:{exc}"}
                        conn.sendall((json.dumps(resp, ensure_ascii=False) + "\n").encode())
                        continue
                    resp = dispatch_daemon_request(session, req, args)
                    conn.sendall((json.dumps(resp, ensure_ascii=False) + "\n").encode())
                    if req.get("cmd") in ("shutdown", "quit"):
                        stop.set()
                        return
        except Exception as exc:  # noqa: BLE001
            try:
                conn.sendall(
                    (json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False) + "\n").encode()
                )
            except Exception:
                pass
        finally:
            try:
                conn.close()
            except Exception:
                pass

    # Background reattach watcher: keep hooks alive across WeChat restarts.
    def reattach_loop() -> None:
        while not stop.is_set():
            # The hook belongs to the Chatlog process that launched it.  This
            # also covers SIGKILL/crash exits where Go never gets a chance to
            # send the normal socket shutdown command.
            if parent_pid > 1 and not process_alive(parent_pid):
                sys.stderr.write(
                    f"[imgdl-daemon] parent exited pid={parent_pid}; stopping\n"
                )
                sys.stderr.flush()
                stop.set()
                break
            try:
                if session._pid is None or not wechat_alive(session._pid or 0):
                    try:
                        session.ensure_attached(0)
                        sys.stderr.write(f"[imgdl-daemon] reattached pid={session._pid}\n")
                        sys.stderr.flush()
                    except Exception:
                        pass
                else:
                    # Soft health check
                    session.status()
            except Exception:
                pass
            stop.wait(3.0)

    threading.Thread(target=reattach_loop, name="reattach", daemon=True).start()

    try:
        while not stop.is_set():
            try:
                conn, _ = server.accept()
            except socket.timeout:
                continue
            except OSError:
                if stop.is_set():
                    break
                continue
            threading.Thread(
                target=handle_client, args=(conn,), name="imgdl-client", daemon=True
            ).start()
    finally:
        try:
            server.close()
        except Exception:
            pass
        session.close()
        try:
            if os.path.exists(sock_path):
                os.unlink(sock_path)
        except OSError:
            pass
        try:
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_UN)
        finally:
            lock_file.close()
        sys.stderr.write("[imgdl-daemon] stopped\n")
    return 0


def dispatch_daemon_request(
    session: FridaSession, req: dict[str, Any], args: argparse.Namespace
) -> dict[str, Any]:
    cmd = str(req.get("cmd") or "").strip().lower()
    req_id = req.get("id")

    if cmd in ("ping", "hello"):
        return {
            "ok": True,
            "cmd": "ping",
            "id": req_id,
            "pid": os.getpid(),
            "parent_pid": int(args.parent_pid or 0),
        }

    if cmd in ("status", "stat"):
        st = session.status()
        st["daemon_pid"] = os.getpid()
        st["parent_pid"] = int(args.parent_pid or 0)
        st["id"] = req_id
        st["cmd"] = "status"
        return st

    if cmd in ("attach", "ensure"):
        try:
            info = session.ensure_attached(int(req.get("pid") or 0))
            info["id"] = req_id
            info["cmd"] = "attach"
            return info
        except Exception as exc:  # noqa: BLE001
            return {"ok": False, "id": req_id, "cmd": "attach", "error": str(exc)}

    if cmd in ("set_upgrade", "setupgrade"):
        try:
            info = session.set_upgrade_receive(bool(req.get("enabled")))
            info["id"] = req_id
            info["cmd"] = "set_upgrade"
            return info
        except Exception as exc:  # noqa: BLE001
            return {"ok": False, "id": req_id, "cmd": "set_upgrade", "error": str(exc)}

    if cmd in ("set_scoped", "setscoped"):
        try:
            info = session.set_scoped_receive(bool(req.get("enabled")))
            info["id"] = req_id
            info["cmd"] = "set_scoped"
            return info
        except Exception as exc:  # noqa: BLE001
            return {"ok": False, "id": req_id, "cmd": "set_scoped", "error": str(exc)}

    if cmd in ("allow_files", "allow"):
        try:
            raw_ids = req.get("file_ids") or []
            if not isinstance(raw_ids, list):
                raw_ids = [raw_ids]
            info = session.allow_scoped_file_ids(
                [str(value) for value in raw_ids],
                float(req.get("ttl_seconds") or 120.0),
            )
            info["id"] = req_id
            info["cmd"] = "allow_files"
            return info
        except Exception as exc:  # noqa: BLE001
            return {"ok": False, "id": req_id, "cmd": "allow_files", "error": str(exc)}

    if cmd in ("set_scope_talkers", "scope_talkers"):
        try:
            raw_talkers = req.get("talkers") or []
            if not isinstance(raw_talkers, list):
                raw_talkers = [raw_talkers]
            info = session.set_scoped_talkers([str(value) for value in raw_talkers])
            info["id"] = req_id
            info["cmd"] = "set_scope_talkers"
            return info
        except Exception as exc:  # noqa: BLE001
            return {"ok": False, "id": req_id, "cmd": "set_scope_talkers", "error": str(exc)}

    if cmd in ("download", "dl"):
        if not args.active_invoke:
            return {
                "ok": False,
                "id": req_id,
                "cmd": "download",
                "error": "synthetic native invocation disabled; receive upgrade is active",
            }
        file_id = str(req.get("file_id") or req.get("fileId") or "")
        aes_key = str(req.get("aes_key") or req.get("aesKey") or "")
        mode = str(req.get("mode") or args.mode or "auto")
        if not file_id or not aes_key:
            return {
                "ok": False,
                "id": req_id,
                "cmd": "download",
                "error": "missing file_id or aes_key",
            }
        # Optional short wait for manager if not yet primed.
        wait_manager = float(req.get("wait_manager_seconds") or 0)
        if wait_manager > 0:
            deadline = time.time() + min(wait_manager, 15.0)
            while time.time() < deadline:
                try:
                    session.ensure_attached(int(req.get("pid") or 0))
                    st = session.status().get("capture") or {}
                    if st.get("has_manager"):
                        break
                except Exception:
                    break
                time.sleep(0.2)
        result = session.download(
            file_id,
            aes_key,
            str(req.get("request_key") or ""),
            str(req.get("media_key") or ""),
            str(req.get("media_path") or ""),
            str(req.get("media_length") or req.get("length") or ""),
            mode,
        )
        result["id"] = req_id
        result["cmd"] = "download"
        wait_file = req.get("wait_file")
        if wait_file and result.get("ok"):
            result["wait"] = poll_path(
                Path(str(wait_file)),
                int(req.get("min_size") or 8000),
                float(req.get("wait_timeout") or 45),
            )
            if not result["wait"].get("ready"):
                result["ok"] = False
                result["error"] = result.get("error") or "download_invoked_but_file_not_ready"
        return result

    if cmd in ("shutdown", "quit"):
        return {"ok": True, "id": req_id, "cmd": "shutdown"}

    return {"ok": False, "id": req_id, "error": f"unknown_cmd:{cmd}"}


def run_oneshot(args: argparse.Namespace) -> int:
    if not args.active_invoke:
        print(
            json.dumps(
                {"ok": False, "error": "synthetic native invocation disabled"},
                ensure_ascii=False,
            )
        )
        return 2
    if not args.file_id or not args.aes_key:
        print(
            json.dumps(
                {"ok": False, "error": "oneshot mode requires --file-id and --aes-key"},
                ensure_ascii=False,
            )
        )
        return 2

    session = FridaSession(
        allow_hash_mismatch=bool(args.allow_hash_mismatch),
        attach_timeout=float(args.attach_timeout),
        invoke_timeout=float(args.invoke_timeout),
    )
    result: dict[str, Any] = {"ok": False}
    try:
        session.ensure_attached(args.pid or 0)
        prime = max(0.0, min(float(args.prime_seconds or 0.0), 8.0))
        if prime > 0:
            deadline = time.time() + prime
            while time.time() < deadline:
                st = session.status().get("capture") or {}
                if st.get("has_manager"):
                    break
                time.sleep(0.15)
        result = session.download(
            args.file_id,
            args.aes_key,
            str(args.request_key or ""),
            str(args.media_key or ""),
            str(args.media_path or ""),
            str(args.media_length or ""),
            args.mode,
        )
        result["daemon"] = False
        if args.wait_file and result.get("ok"):
            result["wait"] = poll_path(args.wait_file, args.min_size, args.wait_timeout)
            if not result["wait"].get("ready"):
                result["ok"] = False
                result["error"] = result.get("error") or "download_invoked_but_file_not_ready"
    except Exception as exc:  # noqa: BLE001
        result = {"ok": False, "error": str(exc)}
    finally:
        session.close()

    code = 0 if result.get("ok") else 2
    emit_result_and_exit(result, code)
    return code


def main() -> int:
    ap = argparse.ArgumentParser(description="Trigger WeChat image download via Frida")
    ap.add_argument("--daemon", action="store_true", help="run long-lived daemon on Unix socket")
    ap.add_argument(
        "--active-invoke",
        action="store_true",
        help="enable bounded native replay for already indexed image messages",
    )
    ap.add_argument("--socket", default=DEFAULT_SOCKET, help="daemon Unix socket path")
    ap.add_argument("--pid", type=int)
    ap.add_argument(
        "--parent-pid",
        type=int,
        default=0,
        help="exit when the owning Chatlog process exits (defaults to current parent)",
    )
    ap.add_argument("--file-id", default="", help="cdnmidimgurl / cdnbigimgurl value")
    ap.add_argument("--aes-key", default="", help="aeskey hex from message XML")
    ap.add_argument("--request-key", default="", help="native talker_timestamp_localid_variant identity")
    ap.add_argument("--media-key", default="", help="local image media key")
    ap.add_argument("--media-path", default="", help="relative local image path")
    ap.add_argument("--media-length", default="", help="expected image length from message XML")
    ap.add_argument("--mode", default="auto", choices=["auto", "create", "startA"])
    ap.add_argument(
        "--prime-seconds",
        type=float,
        default=1.5,
        help="oneshot: wait for passive CDN manager capture after attach",
    )
    ap.add_argument("--attach-timeout", type=float, default=8.0)
    ap.add_argument("--invoke-timeout", type=float, default=8.0)
    ap.add_argument(
        "--trace-requests",
        action="store_true",
        help="passively retain bounded request snapshots for hook comparison",
    )
    ap.add_argument(
        "--upgrade-receive",
        action="store_true",
        help="upgrade incoming image requests from thumbnail variant 3 to normal variant 2",
    )
    ap.add_argument(
        "--scoped-receive",
        action="store_true",
        help="upgrade only file ids registered through daemon allow_files requests",
    )
    ap.add_argument("--wait-file", type=Path, help="filesystem path to wait for after invoke")
    ap.add_argument("--min-size", type=int, default=10_000)
    ap.add_argument("--wait-timeout", type=float, default=45.0)
    ap.add_argument("--allow-hash-mismatch", action="store_true")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    if args.daemon:
        try:
            return run_daemon(args)
        except Exception as exc:  # noqa: BLE001
            print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
            return 2

    return run_oneshot(args)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SystemExit:
        raise
    except Exception as exc:  # noqa: BLE001
        emit_result_and_exit({"ok": False, "error": str(exc)}, 2)
