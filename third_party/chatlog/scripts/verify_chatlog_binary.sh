#!/usr/bin/env bash
set -euo pipefail

binary="${1:?usage: verify_chatlog_binary.sh BINARY GOOS GOARCH VERSION}"
expected_os="${2:?missing expected GOOS}"
expected_arch="${3:?missing expected GOARCH}"
expected_version="${4:?missing expected version}"

if [[ ! -x "${binary}" ]]; then
  printf 'Binary is missing or not executable: %s\n' "${binary}" >&2
  exit 1
fi

metadata="$(go version -m "${binary}")"
require_metadata() {
  local expected="$1"
  if ! grep -Fq $'\tbuild\t'"${expected}" <<<"${metadata}"; then
    printf 'Binary build profile mismatch: expected %s in %s\n' "${expected}" "${binary}" >&2
    printf '%s\n' "${metadata}" >&2
    exit 1
  fi
}

require_metadata '-tags=sqlite_fts5'
require_metadata '-trimpath=true'
require_metadata 'CGO_ENABLED=1'
require_metadata "GOOS=${expected_os}"
require_metadata "GOARCH=${expected_arch}"

if [[ "${CHATLOG_SKIP_EXEC_VERIFY:-0}" != "1" ]]; then
  actual_version="$("${binary}" --version)"
  if [[ "${actual_version}" != "${expected_version}" ]]; then
    printf 'Binary version mismatch: got %q, expected %q\n' \
      "${actual_version}" "${expected_version}" >&2
    exit 1
  fi
fi

printf 'Verified %s: sqlite_fts5, CGO, trimpath, %s/%s, version %s\n' \
  "${binary}" "${expected_os}" "${expected_arch}" "${expected_version}"
