#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

commit_sha="${CI_COMMIT_SHA:-$(git rev-parse HEAD)}"
short_sha="${commit_sha:0:7}"
version="${RELEASE_VERSION:-build-${short_sha}}"

rm -rf dist release
mkdir -p dist release

for arch in amd64 arm64; do
  binary="dist/chatlog-darwin-${arch}"
  package_dir="dist/package-${arch}"
  launcher="${package_dir}/start-chatlog.command"

  CHATLOG_VERSION="${version}" GOOS=darwin GOARCH="${arch}" \
    scripts/build_chatlog_binary.sh "${binary}"

  chmod 755 "${binary}"
  test -x "${binary}"
  "${binary}" --help >/dev/null

  mkdir -p "${package_dir}"
  cp "${binary}" "${package_dir}/chatlog-darwin-${arch}"
  cat >"${launcher}" <<EOF
#!/bin/zsh
set -euo pipefail

script_dir="\$(cd -- "\$(dirname -- "\$0")" && pwd)"
binary="\${script_dir}/chatlog-darwin-${arch}"

chmod 755 "\${binary}"
xattr -d com.apple.quarantine "\${binary}" 2>/dev/null || true
exec "\${binary}" "\$@"
EOF
  chmod 755 "${launcher}"
  /bin/zsh -n "${launcher}"
  "${launcher}" --help >/dev/null

  archive="release/chatlog_${short_sha}_darwin_${arch}.zip"
  /usr/bin/zip -q -j "${archive}" \
    "${package_dir}/chatlog-darwin-${arch}" \
    "${launcher}" \
    LICENSE \
    README.md
  test -s "${archive}"
done

(
  cd release
  /usr/bin/shasum -a 256 \
    chatlog_"${short_sha}"_darwin_amd64.zip \
    chatlog_"${short_sha}"_darwin_arm64.zip \
    > SHA256SUMS
)
/usr/bin/zip -q -j \
  "release/chatlog_${short_sha}_checksums.zip" \
  release/SHA256SUMS
test -s "release/chatlog_${short_sha}_checksums.zip"

printf 'Release files:\n'
ls -lh release/
