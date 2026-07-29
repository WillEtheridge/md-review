#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
SELECTION_DIRECTORY="${PROJECT_DIRECTORY}/spikes/selection-mapping"
GO_VALIDATION_DIRECTORY="${PROJECT_DIRECTORY}/spikes/go-validation"
MDREVIEW_FUZZTIME="${MDREVIEW_FUZZTIME:-5s}"

command -v node >/dev/null
command -v npm >/dev/null
command -v go >/dev/null

(
  cd "${SELECTION_DIRECTORY}"
  npm ci
  npx playwright install chromium firefox
  npm run build
  npm test
)

(
  cd "${GO_VALIDATION_DIRECTORY}"
  go mod download
  go vet ./...
  go test -race -cover ./...
  go test -run=^$ -fuzz=FuzzDecodeDoesNotPanic \
    -fuzztime="${MDREVIEW_FUZZTIME}" ./sidecar
)
