#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
VERSION="${MDREVIEW_VERSION:-v0.2.0-preview.1}"
OUTPUT_DIRECTORY="${1:-${PROJECT_DIRECTORY}/build/macos-preview}"
ARCHIVE_ROOT="mdreview-${VERSION}-darwin-arm64"
ARCHIVE_NAME="${ARCHIVE_ROOT}.tar.gz"
TEMPORARY_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-macos-preview.XXXXXXXX")"
PLACEHOLDER_PATH="${PROJECT_DIRECTORY}/web/dist/placeholder.txt"
PLACEHOLDER_BACKUP="${TEMPORARY_DIRECTORY}/placeholder.txt"
cleanup() {
  if [[ -f "${PLACEHOLDER_BACKUP}" ]]; then
    install -m 0644 "${PLACEHOLDER_BACKUP}" "${PLACEHOLDER_PATH}"
  fi
  rm -rf -- "${TEMPORARY_DIRECTORY}"
}
trap cleanup EXIT

export GOTOOLCHAIN=local
export GOWORK=off
export GOENV=off
export LC_ALL=C
export TZ=UTC
umask 022

command -v go >/dev/null
command -v npm >/dev/null
command -v tar >/dev/null
command -v sha256sum >/dev/null

npm --prefix "${PROJECT_DIRECTORY}/web" ci --no-audit
npm --prefix "${PROJECT_DIRECTORY}/web" run test
npm --prefix "${PROJECT_DIRECTORY}/web" run build
if [[ -f "${PLACEHOLDER_PATH}" ]]; then
  mv -- "${PLACEHOLDER_PATH}" "${PLACEHOLDER_BACKUP}"
fi
"${SCRIPT_DIRECTORY}/verify-release-assets.sh"

PACKAGE_DIRECTORY="${TEMPORARY_DIRECTORY}/${ARCHIVE_ROOT}"
mkdir -p "${PACKAGE_DIRECTORY}/schema"

(
  cd "${PROJECT_DIRECTORY}"
  go test -mod=readonly ./cmd/... ./internal/... ./web
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build \
      -mod=readonly \
      -trimpath \
      -buildvcs=false \
      -ldflags=-buildid= \
      -o "${PACKAGE_DIRECTORY}/mdreview" \
      ./cmd/mdreview
)
chmod 0755 "${PACKAGE_DIRECTORY}/mdreview"

for relative_path in README.md SECURITY.md THIRD_PARTY_NOTICES.md LICENSE; do
  install -m 0644 \
    "${PROJECT_DIRECTORY}/${relative_path}" \
    "${PACKAGE_DIRECTORY}/${relative_path}"
done
install -m 0644 \
  "${PROJECT_DIRECTORY}/schema/review-v1.schema.json" \
  "${PACKAGE_DIRECTORY}/schema/review-v1.schema.json"

mkdir -p "${OUTPUT_DIRECTORY}"
tar \
  --sort=name \
  --mtime="@1785283200" \
  --owner=0 \
  --group=0 \
  --numeric-owner \
  -czf "${OUTPUT_DIRECTORY}/${ARCHIVE_NAME}" \
  -C "${TEMPORARY_DIRECTORY}" \
  "${ARCHIVE_ROOT}"

(
  cd "${OUTPUT_DIRECTORY}"
  sha256sum "${ARCHIVE_NAME}" >SHA256SUMS
  sha256sum -c SHA256SUMS
)

echo "macOS preview written to ${OUTPUT_DIRECTORY}/${ARCHIVE_NAME}"
