#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
OUTPUT_DIRECTORY="${PROJECT_DIRECTORY}/build"

npm --prefix "${PROJECT_DIRECTORY}/web" run build

mkdir -p "${OUTPUT_DIRECTORY}"
(
  cd "${PROJECT_DIRECTORY}"
  go build -trimpath -o "${OUTPUT_DIRECTORY}/mdreview" ./cmd/mdreview
)

