#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
DEFAULT_OUTPUT_DIRECTORY="${PROJECT_DIRECTORY}/build/release-candidate"

if (($# < 1 || $# > 2)); then
  echo "usage: build-release.sh linux/amd64|darwin/arm64 [OUTPUT_DIRECTORY]" >&2
  exit 1
fi

TARGET="$1"
OUTPUT_DIRECTORY="${2:-${DEFAULT_OUTPUT_DIRECTORY}}"
case "${TARGET}" in
linux/amd64)
  TARGET_GOOS="linux"
  TARGET_GOARCH="amd64"
  TARGET_ARCH_ENV=(GOAMD64=v1)
  ;;
darwin/arm64)
  TARGET_GOOS="darwin"
  TARGET_GOARCH="arm64"
  TARGET_ARCH_ENV=()
  ;;
*)
  echo "unsupported release target: ${TARGET}" >&2
  exit 1
  ;;
esac

PLACEHOLDER_PATH="${PROJECT_DIRECTORY}/web/dist/placeholder.txt"
PLACEHOLDER_BACKUP=""
if [[ -f "${PLACEHOLDER_PATH}" ]]; then
  PLACEHOLDER_BACKUP="$(mktemp "${TMPDIR:-/tmp}/mdreview-placeholder.XXXXXXXX")"
  install -m 0644 "${PLACEHOLDER_PATH}" "${PLACEHOLDER_BACKUP}"
fi
restore_placeholder() {
  if [[ -n "${PLACEHOLDER_BACKUP}" ]]; then
    mkdir -p "$(dirname -- "${PLACEHOLDER_PATH}")"
    install -m 0644 "${PLACEHOLDER_BACKUP}" "${PLACEHOLDER_PATH}"
    rm -f -- "${PLACEHOLDER_BACKUP}"
  fi
}
trap restore_placeholder EXIT

export GOTOOLCHAIN=local
export GOWORK=off
export GOENV=off
export LC_ALL=C
export TZ=UTC
umask 022

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null

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

rm -f -- "${PLACEHOLDER_PATH}"
"${SCRIPT_DIRECTORY}/verify-release-assets.sh"

(
  cd "${PROJECT_DIRECTORY}"
  go test -mod=readonly ./cmd/... ./internal/... ./web
  env \
    CGO_ENABLED=0 \
    GOOS="${TARGET_GOOS}" \
    GOARCH="${TARGET_GOARCH}" \
    "${TARGET_ARCH_ENV[@]}" \
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
  "${OUTPUT_DIRECTORY}" \
  "${TARGET}"

"${SCRIPT_DIRECTORY}/release/verify-binary.sh" \
  "${OUTPUT_DIRECTORY}/mdreview" \
  "${TARGET}"
