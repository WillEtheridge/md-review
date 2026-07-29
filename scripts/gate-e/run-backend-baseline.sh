#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/../.." && pwd)"

command -v go >/dev/null
command -v node >/dev/null

if (($# > 1)); then
  echo "usage: $0 [output-directory]" >&2
  exit 2
fi

if (($# == 1)); then
  OUTPUT_DIRECTORY="$(realpath -m -- "$1")"
  if [[ -e "${OUTPUT_DIRECTORY}" ]] && [[ -n "$(find "${OUTPUT_DIRECTORY}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Gate E output directory must be empty: ${OUTPUT_DIRECTORY}" >&2
    exit 2
  fi
  mkdir -p -- "${OUTPUT_DIRECTORY}"
else
  OUTPUT_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-gate-e-backend.XXXXXXXX")"
fi

chmod 0700 "${OUTPUT_DIRECTORY}"
BINARY_PATH="${OUTPUT_DIRECTORY}/mdreview"

(
  cd "${PROJECT_DIRECTORY}"
  CGO_ENABLED=0 go build -trimpath -o "${BINARY_PATH}" ./cmd/mdreview
)

node "${SCRIPT_DIRECTORY}/measure-backend.mjs" \
  "${PROJECT_DIRECTORY}" \
  "${OUTPUT_DIRECTORY}" \
  "${BINARY_PATH}"

echo "Gate E backend baseline: ${OUTPUT_DIRECTORY}/backend-baseline.json"
