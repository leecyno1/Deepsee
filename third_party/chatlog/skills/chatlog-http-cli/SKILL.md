---
name: chatlog-http-cli
description: Use Chatlog's machine-readable CLI to manage the local service and call its complete HTTP endpoint catalog.
---

# Chatlog structured CLI

Read `docs/LLM_CLI.md` before invoking commands.

## Required workflow

1. Run `chatlog ops accounts` and select a target with `--pid` or `--account`.
2. Use `chatlog ops` for keys, decryption, service lifecycle, and persistent configuration.
3. Run `chatlog api catalog --compact`, then `chatlog api describe <endpoint>` before calling it.
4. Call only `chatlog api call <endpoint>` aliases returned by the catalog; never invent parameters.
5. Parse stdout as JSON (`api`) or JSON Lines (`ops`) and check the process exit code.
6. Follow `response.mode`: `binary` needs `--output`; `media` needs it unless `info=1`; `format` needs it for `csv/xlsx/excel`.
7. Pass `--confirm` only after explicit authorization when `confirmation_required` is true.
8. Use documented time ranges such as `last-7d`, `last-30d`, `today`, or an explicit date interval; `7d`/`30d` are invalid.
9. Stop `chatlog ops server-start` with SIGINT/SIGTERM and wait for the final JSONL event with `stage=stopped`; it includes `elapsed_ms`.

## Examples

```bash
chatlog ops status --account wxid_example
chatlog api catalog --compact
chatlog api describe history
chatlog api call sessions --param limit=20
chatlog api call history --param chat=filehelper --param time=last-30d --param limit=100
chatlog api call image --path-param key=<media-key> --output ./image.jpg
```

Never expose the loopback control plane, log secrets, guess raw paths, or retry side-effecting operations indefinitely.
