#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
BACKEND_BASELINE_OUTPUT="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-m7-backend.XXXXXXXX")"
IMAGE_BASELINE_OUTPUT="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-m7-images.XXXXXXXX")"
PACKAGE_EXTRACT_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-m7-package.XXXXXXXX")"
trap 'rm -rf -- "${BACKEND_BASELINE_OUTPUT}" "${IMAGE_BASELINE_OUTPUT}" "${PACKAGE_EXTRACT_DIRECTORY}"' EXIT

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null
command -v python3 >/dev/null
command -v readelf >/dev/null
command -v sha256sum >/dev/null
command -v tar >/dev/null

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "Milestone 7 requires Go 1.26.5" >&2
  exit 1
fi
if [[ "$(node --version)" != "v26.2.0" ]]; then
  echo "Milestone 7 requires Node.js 26.2.0" >&2
  exit 1
fi
if [[ "$(npm --version)" != "11.13.0" ]]; then
  echo "Milestone 7 requires npm 11.13.0" >&2
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
node --test "${SCRIPT_DIRECTORY}/gate-e/verify-thresholds.test.mjs"
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
node \
  "${SCRIPT_DIRECTORY}/validate-skill-evaluation.mjs" \
  "${PROJECT_DIRECTORY}/testdata/integration/m6-skill" \
  "${PROJECT_DIRECTORY}/testdata/integration/m6-skill-result" \
  "${PROJECT_DIRECTORY}/internal/skillassets/mdreview/SKILL.md"

"${SCRIPT_DIRECTORY}/gate-e/verify-resource-counters.sh"
(
  cd "${PROJECT_DIRECTORY}"
  go test -run='^$' -fuzz='^FuzzDecodeDoesNotPanic$' -fuzztime=3s ./internal/review
)
"${SCRIPT_DIRECTORY}/validate-spikes.sh"

"${SCRIPT_DIRECTORY}/test-package-release.sh"
"${SCRIPT_DIRECTORY}/package-release.sh"
RELEASE_DIRECTORY="${PROJECT_DIRECTORY}/build/release"
SOURCE_ARCHIVE="${RELEASE_DIRECTORY}/mdreview-v0.1.0-source.tar.gz"
BINARY_ARCHIVE="${RELEASE_DIRECTORY}/mdreview-v0.1.0-linux-amd64.tar.gz"
RELEASE_SBOM="${RELEASE_DIRECTORY}/mdreview-v0.1.0-linux-amd64.spdx.json"
CHECKSUMS="${RELEASE_DIRECTORY}/SHA256SUMS"
for artifact in "${SOURCE_ARCHIVE}" "${BINARY_ARCHIVE}" "${RELEASE_SBOM}" "${CHECKSUMS}"; do
  if [[ ! -f "${artifact}" ]]; then
    echo "Milestone 7 package is missing ${artifact}" >&2
    exit 1
  fi
done
(
  cd "${RELEASE_DIRECTORY}"
  sha256sum -c SHA256SUMS
)

tar -xzf "${BINARY_ARCHIVE}" -C "${PACKAGE_EXTRACT_DIRECTORY}"
PACKAGED_BINARY="${PACKAGE_EXTRACT_DIRECTORY}/mdreview-v0.1.0-linux-amd64/mdreview"
"${SCRIPT_DIRECTORY}/release/verify-binary.sh" "${PACKAGED_BINARY}"
(
  cd "${PROJECT_DIRECTORY}/web"
  MDREVIEW_GO_SERVER_BINARY="${PACKAGED_BINARY}" \
    npx playwright test \
      --config playwright.go-server.config.ts \
      tests/go-server/m7-security.spec.ts \
      tests/go-server/m7-agent-handoff.spec.ts
)

node \
  "${SCRIPT_DIRECTORY}/gate-e/measure-backend.mjs" \
  "${PROJECT_DIRECTORY}" \
  "${BACKEND_BASELINE_OUTPUT}" \
  "${PACKAGED_BINARY}"
node \
  "${SCRIPT_DIRECTORY}/gate-e/measure-images.mjs" \
  "${PROJECT_DIRECTORY}" \
  "${IMAGE_BASELINE_OUTPUT}" \
  "${PACKAGED_BINARY}"
node \
  "${SCRIPT_DIRECTORY}/gate-e/verify-thresholds.mjs" \
  "${BACKEND_BASELINE_OUTPUT}/backend-baseline.json" \
  "${IMAGE_BASELINE_OUTPUT}/image-baseline.json" \
  "${PACKAGED_BINARY}"

RELEASE_EVIDENCE_DIRECTORY="${RELEASE_DIRECTORY}/evidence"
mkdir -p "${RELEASE_EVIDENCE_DIRECTORY}"
install -m 0644 \
  "${BACKEND_BASELINE_OUTPUT}/backend-baseline.json" \
  "${RELEASE_EVIDENCE_DIRECTORY}/backend-baseline.json"
install -m 0644 \
  "${IMAGE_BASELINE_OUTPUT}/image-baseline.json" \
  "${RELEASE_EVIDENCE_DIRECTORY}/image-baseline.json"
(
  cd "${RELEASE_EVIDENCE_DIRECTORY}"
  sha256sum backend-baseline.json image-baseline.json >SHA256SUMS
  sha256sum -c SHA256SUMS
)

echo "Milestone 7 automated verification passed against the exact packaged binary."
echo "Gate E reports retained in ${RELEASE_EVIDENCE_DIRECTORY}."
