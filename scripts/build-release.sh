#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
DEFAULT_OUTPUT_DIRECTORY="${PROJECT_DIRECTORY}/build/release-candidate"
OUTPUT_DIRECTORY="${1:-${DEFAULT_OUTPUT_DIRECTORY}}"

export GOTOOLCHAIN=local
export GOWORK=off
export GOENV=off
export LC_ALL=C
export TZ=UTC
umask 022

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null
command -v readelf >/dev/null

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "release build requires Go 1.26.5" >&2
  exit 1
fi
if [[ "$(node --version)" != "v26.2.0" ]]; then
  echo "release build requires Node.js 26.2.0" >&2
  exit 1
fi
if [[ "$(npm --version)" != "11.13.0" ]]; then
  echo "release build requires npm 11.13.0" >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIRECTORY}"
if find "${OUTPUT_DIRECTORY}" -mindepth 1 -print -quit | grep -q .; then
  echo "release build output directory must be empty: ${OUTPUT_DIRECTORY}" >&2
  exit 1
fi

npm --prefix "${PROJECT_DIRECTORY}/web" ci --no-audit
npm --prefix "${PROJECT_DIRECTORY}/web" run test
npm --prefix "${PROJECT_DIRECTORY}/web" run build

# The tracked marker makes go:embed valid in a source archive. It is removed
# only after Vite succeeds, so no release compilation can retain the scaffold.
rm -f -- "${PROJECT_DIRECTORY}/web/dist/placeholder.txt"
"${SCRIPT_DIRECTORY}/verify-release-assets.sh"

(
  cd "${PROJECT_DIRECTORY}"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
    go test -mod=readonly ./cmd/... ./internal/... ./web
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
    go build \
      -mod=readonly \
      -trimpath \
      -buildvcs=false \
      -ldflags=-buildid= \
      -o "${OUTPUT_DIRECTORY}/mdreview" \
      ./cmd/mdreview
)
chmod 0755 "${OUTPUT_DIRECTORY}/mdreview"

RELEASE_DOCUMENTS=(
  README.md
  SECURITY.md
  THIRD_PARTY_NOTICES.md
  LICENSE
  schema/review-v1.schema.json
)
for relative_path in "${RELEASE_DOCUMENTS[@]}"; do
  if [[ ! -f "${PROJECT_DIRECTORY}/${relative_path}" ]]; then
    echo "release build is missing required file: ${relative_path}" >&2
    exit 1
  fi
  install -D -m 0644 \
    "${PROJECT_DIRECTORY}/${relative_path}" \
    "${OUTPUT_DIRECTORY}/${relative_path}"
done

node \
  "${SCRIPT_DIRECTORY}/release/metadata.mjs" \
  "${PROJECT_DIRECTORY}" \
  "${OUTPUT_DIRECTORY}/mdreview" \
  "${OUTPUT_DIRECTORY}"

"${SCRIPT_DIRECTORY}/release/verify-binary.sh" "${OUTPUT_DIRECTORY}/mdreview"
