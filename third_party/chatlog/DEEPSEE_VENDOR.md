# Vendored Chatlog dependency

This directory contains the buildable Chatlog source used by Deepsee's local
WeChat history sidecar.

- Current upstream: `https://gitflic.ru/project/goring/chatlog_alpha`
- Imported commit: `cc281cc5fc54c14c4f3f58ee48dab11e538eadac`
- Imported on 2026-08-28. This revision replaces the obsolete macOS process
  memory pattern scan with Frida capture of `CCKeyDerivationPBKDF`, validates
  per-database keys, and reads encrypted WCDB files directly.
- Deepsee carries a small macOS startup-race fix: after LaunchServices creates
  the WeChat process, Frida waits one second before attaching. Attaching on the
  first observable process tick could block `script.load()` on WeChat 4.1.80
  and miss the database-key derivation during login.
- The former GitHub repositories no longer publish source or release artifacts.
- License and usage restrictions: see `LICENSE` and `DISCLAIMER.md` in this
  directory.

Deepsee does not modify the Go module path. Build it with:

```bash
bash scripts/build_chatlog.sh
```

The generated executable is written to `.local/chatlog/bin/` and is intentionally
not committed. Database-key extraction additionally requires a Python environment
that can import `frida`; Deepsee installs it under `.local/chatlog-frida/`.
