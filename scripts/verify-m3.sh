#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "Milestone 3 requires Go 1.26.5" >&2
  exit 1
fi

if [[ "$(node --version)" != "v26.2.0" ]]; then
  echo "Milestone 3 requires Node.js 26.2.0" >&2
  exit 1
fi

if [[ "$(npm --version)" != "11.13.0" ]]; then
  echo "Milestone 3 requires npm 11.13.0" >&2
  exit 1
fi

npm --prefix "${PROJECT_DIRECTORY}/web" ci
(
  cd "${PROJECT_DIRECTORY}/web"
  PLAYWRIGHT_INSTALL_ARGUMENTS=(install chromium firefox)
  if [[ "${CI:-}" == "true" ]]; then
    PLAYWRIGHT_INSTALL_ARGUMENTS=(install --with-deps chromium firefox)
  fi
  npx playwright "${PLAYWRIGHT_INSTALL_ARGUMENTS[@]}"
)

"${SCRIPT_DIRECTORY}/check.sh"
"${SCRIPT_DIRECTORY}/test.sh"
(
  cd "${PROJECT_DIRECTORY}"
  go test -run='^$' -fuzz='^FuzzDecodeDoesNotPanic$' -fuzztime=3s ./internal/review
)
"${SCRIPT_DIRECTORY}/validate-spikes.sh"

if "${SCRIPT_DIRECTORY}/verify-release-assets.sh"; then
  echo "Milestone 3 expected release verification to remain blocked by deferred assets" >&2
  exit 1
fi

echo "Milestone 3 verification passed; deferred Agent Skill/release assets remain blocked."
