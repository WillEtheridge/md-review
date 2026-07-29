#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
BACKEND_BASELINE_OUTPUT="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-m6-backend.XXXXXXXX")"
IMAGE_BASELINE_OUTPUT="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-m6-images.XXXXXXXX")"
trap 'rm -rf -- "${BACKEND_BASELINE_OUTPUT}" "${IMAGE_BASELINE_OUTPUT}"' EXIT

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null
command -v python3 >/dev/null

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "Milestone 6 requires Go 1.26.5" >&2
  exit 1
fi

if [[ "$(node --version)" != "v26.2.0" ]]; then
  echo "Milestone 6 requires Node.js 26.2.0" >&2
  exit 1
fi

if [[ "$(npm --version)" != "11.13.0" ]]; then
  echo "Milestone 6 requires npm 11.13.0" >&2
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
  go test -race -count=3 ./internal/runtime ./internal/skills
)
SKILL_VALIDATOR="${MDREVIEW_SKILL_VALIDATOR:-${HOME}/.codex/skills/.system/skill-creator/scripts/quick_validate.py}"
if [[ -f "${SKILL_VALIDATOR}" ]]; then
  python3 \
    "${SKILL_VALIDATOR}" \
    "${PROJECT_DIRECTORY}/internal/skillassets/mdreview"
else
  echo "Skill Creator validator unavailable; canonical frontmatter and workflow remain covered by Go tests."
fi
node --check "${SCRIPT_DIRECTORY}/validate-skill-evaluation.mjs"
node \
  "${SCRIPT_DIRECTORY}/validate-skill-evaluation.mjs" \
  "${PROJECT_DIRECTORY}/testdata/integration/m6-skill" \
  "${PROJECT_DIRECTORY}/testdata/integration/m6-skill-result" \
  "${PROJECT_DIRECTORY}/internal/skillassets/mdreview/SKILL.md"
"${SCRIPT_DIRECTORY}/gate-e/verify-resource-counters.sh"
"${SCRIPT_DIRECTORY}/gate-e/run-backend-baseline.sh" "${BACKEND_BASELINE_OUTPUT}"
"${SCRIPT_DIRECTORY}/gate-e/run-image-baseline.sh" "${IMAGE_BASELINE_OUTPUT}"
(
  cd "${PROJECT_DIRECTORY}"
  go test -run='^$' -fuzz='^FuzzDecodeDoesNotPanic$' -fuzztime=3s ./internal/review
)
"${SCRIPT_DIRECTORY}/validate-spikes.sh"

release_output=""
if release_output="$("${SCRIPT_DIRECTORY}/verify-release-assets.sh" 2>&1)"; then
  echo "Milestone 6 expected release verification to remain blocked by the deferred frontend marker" >&2
  exit 1
fi
if [[ "${release_output}" != *"the frontend is still a placeholder"* ]]; then
  echo "Milestone 6 release guard failed for an unexpected reason:" >&2
  echo "${release_output}" >&2
  exit 1
fi

echo "Milestone 6 verification passed; only the deferred frontend release marker remains blocked."
