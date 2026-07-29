#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"

"${SCRIPT_DIRECTORY}/test-release-assets.sh"

(
  cd "${PROJECT_DIRECTORY}"
  # See check.sh: an unrestricted ./... would include npm dependency examples.
  go test -race ./cmd/... ./internal/... ./web
)

npm --prefix "${PROJECT_DIRECTORY}/web" run test:unit
npm --prefix "${PROJECT_DIRECTORY}/web" run test:fonts
npm --prefix "${PROJECT_DIRECTORY}/web" run test:browser
"${SCRIPT_DIRECTORY}/test-go-server.sh"
npm --prefix "${PROJECT_DIRECTORY}/web" run test:visual
