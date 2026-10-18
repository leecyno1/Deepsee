#!/usr/bin/env bash
set -euo pipefail

release_dir="${1:-release}"
token="${GITFLIC_RELEASE_TOKEN:-${CI_JOB_TOKEN:-}}"
api_root="${GITFLIC_API_ROOT:-${CI_SERVER_REST_URL:-https://api.gitflic.ru}}"
owner="${CI_PROJECT_NAMESPACE:-goring}"
project="${CI_PROJECT_NAME:-chatlog_alpha}"
commit_sha="${CI_COMMIT_SHA:-$(git rev-parse HEAD)}"
short_sha="${commit_sha:0:7}"
tag_name="${GITFLIC_RELEASE_TAG:-build-${commit_sha:0:12}}"
release_title="${GITFLIC_RELEASE_TITLE:-Chatlog macOS ${short_sha}}"
project_api="${api_root%/}/project/${owner}/${project}"

if [[ -z "${token}" ]]; then
  printf 'GITFLIC_RELEASE_TOKEN or CI_JOB_TOKEN is required\n' >&2
  exit 2
fi
if [[ ! -d "${release_dir}" ]]; then
  printf 'Release directory does not exist: %s\n' "${release_dir}" >&2
  exit 2
fi

required=(
  "${release_dir}/SHA256SUMS"
  "${release_dir}/chatlog_${short_sha}_checksums.zip"
)
for file in "${required[@]}"; do
  test -s "${file}"
done
shopt -s nullglob
archives=("${release_dir}"/chatlog_"${short_sha}"_darwin_*.zip)
if [[ "${#archives[@]}" -ne 2 ]]; then
  printf 'Expected two macOS archives for %s; found %s\n' "${short_sha}" "${#archives[@]}" >&2
  exit 2
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/chatlog-gitflic-release.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

api_call() {
  local method="$1"
  local url="$2"
  local output="$3"
  shift 3
  local status
  status="$(curl \
    --http1.1 \
    --retry 6 \
    --retry-all-errors \
    --retry-delay 2 \
    --silent \
    --show-error \
    --output "${output}" \
    --write-out '%{http_code}' \
    --request "${method}" \
    --header "Authorization: token ${token}" \
    "$@" \
    "${url}")"
  printf '%s' "${status}"
}

json_payload() {
  local output="$1"
  local kind="$2"
  python3 - "${output}" "${kind}" "${tag_name}" "${commit_sha}" "${release_title}" <<'PY'
import json
import os
import sys

output, kind, tag, commit, title = sys.argv[1:]
if kind == "tag":
    payload = {
        "tagName": tag,
        "commitId": commit,
        "message": f"Automated build for {commit}",
    }
else:
    ref = os.environ.get("CI_COMMIT_REF_NAME", "local")
    pipeline = os.environ.get("CI_PIPELINE_IID", "local")
    payload = {
        "title": title,
        "description": (
            f"Automated macOS build for `{commit}` from `{ref}` "
            f"(pipeline #{pipeline})."
        ),
        "tagName": tag,
        "preRelease": True,
        "attachProjectArchive": False,
    }
with open(output, "w", encoding="utf-8") as stream:
    json.dump(payload, stream, ensure_ascii=False)
PY
}

tag_response="${tmp_dir}/tag.json"
tag_list="${tmp_dir}/tags.json"
tag_status="$(api_call GET "${project_api}/tag?size=100" "${tag_list}")"
if [[ "${tag_status}" != 200 ]]; then
  cat "${tag_list}" >&2
  printf '\nFailed to list GitFlic tags (HTTP %s)\n' "${tag_status}" >&2
  exit 1
fi
existing_commit="$(python3 - "${tag_list}" "${tag_name}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
tag = sys.argv[2]
items = data.get("_embedded", {}).get("tagList", [])
match = next((item for item in items if item.get("name") == tag), None)
print(match.get("commitId", "") if match else "")
PY
)"
if [[ -n "${existing_commit}" ]]; then
  if [[ "${existing_commit}" != "${commit_sha}" ]]; then
    printf 'Tag %s already points to %s, expected %s\n' "${tag_name}" "${existing_commit}" "${commit_sha}" >&2
    exit 1
  fi
else
  tag_payload="${tmp_dir}/tag-payload.json"
  json_payload "${tag_payload}" tag
  tag_status="$(api_call POST "${project_api}/tag/create" "${tag_response}" \
    --header 'Content-Type: application/json' \
    --data-binary "@${tag_payload}")"
  if [[ "${tag_status}" != 200 ]]; then
    cat "${tag_response}" >&2
    printf '\nFailed to create GitFlic tag (HTTP %s)\n' "${tag_status}" >&2
    exit 1
  fi
fi

release_list="${tmp_dir}/releases.json"
list_status="$(api_call GET "${project_api}/release?size=100" "${release_list}")"
if [[ "${list_status}" != 200 ]]; then
  cat "${release_list}" >&2
  printf '\nFailed to list GitFlic releases (HTTP %s)\n' "${list_status}" >&2
  exit 1
fi
release_id="$(python3 - "${release_list}" "${tag_name}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
tag = sys.argv[2]

def walk(value):
    if isinstance(value, dict):
        if value.get("tagName") == tag and value.get("id"):
            yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)

match = next(walk(data), None)
print(match["id"] if match else "")
PY
)"

release_payload="${tmp_dir}/release-payload.json"
json_payload "${release_payload}" release
release_response="${tmp_dir}/release.json"
if [[ -n "${release_id}" ]]; then
  current_status="$(api_call GET "${project_api}/release/${release_id}" "${release_response}")"
  if [[ "${current_status}" != 200 ]]; then
    cat "${release_response}" >&2
    exit 1
  fi
  attachment_payload="${tmp_dir}/attachments.json"
  python3 - "${release_response}" "${attachment_payload}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
ids = [item["uuid"] for item in data.get("attachmentFiles") or [] if item.get("uuid")]
json.dump(ids, open(sys.argv[2], "w", encoding="utf-8"))
PY
  if [[ "$(cat "${attachment_payload}")" != '[]' ]]; then
    delete_status="$(api_call DELETE "${project_api}/release/${release_id}/file" "${tmp_dir}/delete-files.json" \
      --header 'Content-Type: application/json' \
      --data-binary "@${attachment_payload}")"
    if [[ "${delete_status}" != 200 ]]; then
      cat "${tmp_dir}/delete-files.json" >&2
      exit 1
    fi
  fi
  release_status="$(api_call PUT "${project_api}/release/${release_id}" "${release_response}" \
    --header 'Content-Type: application/json' \
    --data-binary "@${release_payload}")"
else
  release_status="$(api_call POST "${project_api}/release" "${release_response}" \
    --header 'Content-Type: application/json' \
    --data-binary "@${release_payload}")"
fi
if [[ "${release_status}" != 200 ]]; then
  cat "${release_response}" >&2
  printf '\nFailed to create or update GitFlic release (HTTP %s)\n' "${release_status}" >&2
  exit 1
fi
release_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "${release_response}")"

files=(
  "${archives[@]}"
  "${release_dir}/chatlog_${short_sha}_checksums.zip"
)
for file in "${files[@]}"; do
  case "${file}" in
    *.zip) media_type="application/zip" ;;
    *) media_type="application/octet-stream" ;;
  esac
  upload_response="${tmp_dir}/upload-$(basename "${file}").json"
  upload_status="$(api_call POST "${project_api}/release/${release_id}/file" "${upload_response}" \
    --form "files=@${file};type=${media_type}")"
  if [[ "${upload_status}" != 200 ]]; then
    cat "${upload_response}" >&2
    printf '\nFailed to upload %s (HTTP %s)\n' "${file}" "${upload_status}" >&2
    exit 1
  fi
  printf 'Uploaded %s\n' "$(basename "${file}")"
done

verify_response="${tmp_dir}/verify.json"
verify_status="$(api_call GET "${project_api}/release/${release_id}" "${verify_response}")"
if [[ "${verify_status}" != 200 ]]; then
  cat "${verify_response}" >&2
  exit 1
fi
python3 - "${verify_response}" "${#files[@]}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
expected = int(sys.argv[2])
files = data.get("attachmentFiles") or []
if len(files) != expected:
    raise SystemExit(f"release verification failed: expected {expected} files, got {len(files)}")
for item in files:
    if not item.get("hashSha256") or not item.get("size"):
        raise SystemExit(f"release verification failed for {item.get('name')}")
print(f"Verified release {data['tagName']} with {len(files)} files")
PY

printf 'Release URL: https://gitflic.ru/project/%s/%s/release/%s\n' \
  "${owner}" "${project}" "${release_id}"
