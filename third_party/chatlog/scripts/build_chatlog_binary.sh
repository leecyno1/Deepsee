#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

output="${1:-bin/chatlog}"
target_os="${GOOS:-$(go env GOOS)}"
target_arch="${GOARCH:-$(go env GOARCH)}"
version="${CHATLOG_VERSION:-$(git describe --tags --always --dirty='-dev' 2>/dev/null || printf unknown)}"

if [[ "${version}" =~ [[:space:]] ]]; then
  printf 'CHATLOG_VERSION must not contain whitespace: %q\n' "${version}" >&2
  exit 2
fi

mkdir -p "$(dirname -- "${output}")"

printf 'Building %s (%s/%s, version %s)\n' \
  "${output}" "${target_os}" "${target_arch}" "${version}"

# Clear caller-provided GOFLAGS for the build itself. Every generated binary
# must use the same complete profile as `make run`, independent of shell or CI
# environment settings.
GOFLAGS='' \
CGO_ENABLED=1 \
GOOS="${target_os}" \
GOARCH="${target_arch}" \
go build \
  -tags sqlite_fts5 \
  -trimpath \
  -ldflags "-s -w -X github.com/sjzar/chatlog/pkg/version.Version=${version}" \
  -o "${output}" \
  main.go

chmod 755 "${output}"
scripts/verify_chatlog_binary.sh \
  "${output}" "${target_os}" "${target_arch}" "${version}"
