#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"

command -v go >/dev/null
command -v gofmt >/dev/null
command -v node >/dev/null
command -v npm >/dev/null

mapfile -d '' GO_FILES < <(
  find \
    "${PROJECT_DIRECTORY}/cmd" \
    "${PROJECT_DIRECTORY}/internal" \
    -type f -name '*.go' -print0 2>/dev/null
  find "${PROJECT_DIRECTORY}/web" -maxdepth 1 -type f -name '*.go' -print0
)

if ((${#GO_FILES[@]} > 0)); then
  UNFORMATTED_FILES="$(gofmt -l "${GO_FILES[@]}")"
  if [[ -n "${UNFORMATTED_FILES}" ]]; then
    echo "Go files require gofmt:"
    echo "${UNFORMATTED_FILES}"
    exit 1
  fi
fi

while IFS= read -r -d '' SHELL_SCRIPT; do
  bash -n "${SHELL_SCRIPT}"
done < <(find "${PROJECT_DIRECTORY}/scripts" -type f -name '*.sh' -print0)

(
  cd "${PROJECT_DIRECTORY}"
  # web is both a Go embed package and an npm project. Explicit package roots
  # keep Go examples inside node_modules outside mdReview's analysis scope.
  go vet ./cmd/... ./internal/... ./web
)

npm --prefix "${PROJECT_DIRECTORY}/web" run format:check
npm --prefix "${PROJECT_DIRECTORY}/web" run lint
npm --prefix "${PROJECT_DIRECTORY}/web" run typecheck
npm --prefix "${PROJECT_DIRECTORY}/web" run verify:fonts
