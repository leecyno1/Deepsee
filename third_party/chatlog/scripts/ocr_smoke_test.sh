#!/usr/bin/env bash
set -euo pipefail

endpoint="${CHATLOG_OCR_ENDPOINT:-http://127.0.0.1:8081/glmocr/parse}"
image_path="${1:-}"
if [[ -z "${image_path}" || ! -f "${image_path}" ]]; then
  printf 'usage: %s IMAGE.png\n' "$0" >&2
  exit 2
fi

mime_type="$(file -b --mime-type "${image_path}")"
case "${mime_type}" in
  image/jpeg|image/png) ;;
  *)
    printf 'smoke test expects JPEG or PNG, got %s\n' "${mime_type}" >&2
    exit 2
    ;;
esac

encoded="$(base64 < "${image_path}" | tr -d '\n')"
payload="$(python3 -c 'import json,sys; print(json.dumps({"images":[sys.argv[1]]}))' "data:${mime_type};base64,${encoded}")"
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  --data-binary "${payload}" \
  "${endpoint}" \
  | python3 -m json.tool
