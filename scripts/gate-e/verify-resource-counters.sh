#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/../.." && pwd)"
WEB_DIRECTORY="${PROJECT_DIRECTORY}/web"
IMAGE_MANAGER_TEST="${WEB_DIRECTORY}/src/images/manager.test.ts"
VITEST="${WEB_DIRECTORY}/node_modules/.bin/vitest"

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "Gate E resource counters require Go 1.26.5" >&2
  exit 1
fi
if [[ "$(node --version)" != "v26.2.0" ]]; then
  echo "Gate E resource counters require Node.js 26.2.0" >&2
  exit 1
fi
if [[ "$(npm --version)" != "11.13.0" ]]; then
  echo "Gate E resource counters require npm 11.13.0" >&2
  exit 1
fi
if [[ ! -x "${VITEST}" ]]; then
  echo "Gate E resource counters require the pinned web dependencies" >&2
  exit 1
fi

require_go_tests() {
  local package="$1"
  shift
  local names=("$@")
  local joined
  local listed

  joined="$(IFS='|'; echo "${names[*]}")"
  listed="$(
    cd "${PROJECT_DIRECTORY}"
    go test -list "^(${joined})$" "${package}"
  )"
  for name in "${names[@]}"; do
    local count
    count="$(grep -Fxc -- "${name}" <<<"${listed}" || true)"
    if [[ "${count}" != "1" ]]; then
      echo "expected exactly one ${package} test named ${name}; found ${count}" >&2
      exit 1
    fi
  done

  (
    cd "${PROJECT_DIRECTORY}"
    go test -race -count=1 -run "^(${joined})$" "${package}"
  )
}

require_go_tests \
  ./internal/limits \
  TestContentLimits \
  TestImageConcurrencyLimits \
  TestConcurrentImageLoadLimit

require_go_tests \
  ./internal/gatee \
  TestCountersRecordContentAndCompletedScans \
  TestCountersTrackConcurrentAssetGaugeAndBytes

require_go_tests \
  ./internal/workspace \
  TestMeasurementsCountScansContentReadsAndIgnoreReuse \
  TestReadAssetReaderObservesContainedFileGrowthPastLimit \
  TestSnapshotFreshnessExactBoundaryAndNoRequestWork \
  TestSnapshotCoalescesConcurrentStaleRequests \
  TestSnapshotPublishesDocumentAndSidecarMetadataTransitions \
  TestScanReusesAndPrunesSignatureKeyedIgnoreRules

require_go_tests \
  ./internal/review \
  TestStoreMeasurementsCountMarkdownAndSidecarContentReads

require_go_tests \
  ./internal/server \
  TestGateECounterEndpointIsOptIn \
  TestWorkspaceAssetAbortsGrowthBeyondLimit \
  TestWorkspaceAssetGrowthMakesRealHTTPFetchFail \
  TestWorkspaceAssetGlobalSemaphoreCapsEightStreams

FRONTEND_TEST_NAMES=(
  "loads only near-viewport resources in FIFO order with four active requests"
  "coalesces duplicate references into one blob and one object URL"
  "evicts deterministic LRU blobs and waits for a new intersection epoch"
  "retains exactly 40 MiB without eviction"
  "supports explicit retry and ready-resource reload without leaking a URL"
  "does not admit a stale completion when an active reload cannot abort immediately"
  "turns a matching decode failure into corrupt state and revokes once"
  "aborts active work, drops the queue, revokes ready URLs, and clears subscribers"
  "ignores a late successful completion after disposal"
)

for name in "${FRONTEND_TEST_NAMES[@]}"; do
  count="$(
    {
      grep -Fo -- "it(\"${name}\"" "${IMAGE_MANAGER_TEST}" || true
    } | wc -l
  )"
  if [[ "${count//[[:space:]]/}" != "1" ]]; then
    echo "expected exactly one image-manager test named ${name}; found ${count}" >&2
    exit 1
  fi
done

FRONTEND_PATTERN="$(IFS='|'; echo "${FRONTEND_TEST_NAMES[*]}")"
REPORT_PATH="$(mktemp "${TMPDIR:-/tmp}/mdreview-resource-counters.XXXXXXXX.json")"
trap 'rm -- "${REPORT_PATH}"' EXIT

(
  cd "${WEB_DIRECTORY}"
  "${VITEST}" run \
    src/images/manager.test.ts \
    --testNamePattern "${FRONTEND_PATTERN}" \
    --reporter=json \
    --outputFile="${REPORT_PATH}"
)

node - "${REPORT_PATH}" "${#FRONTEND_TEST_NAMES[@]}" <<'NODE'
const { readFileSync } = require("node:fs");

const reportPath = process.argv[2];
const expectedCount = Number(process.argv[3]);
const report = JSON.parse(readFileSync(reportPath, "utf8"));
if (
  report.success !== true ||
  report.numPassedTests !== expectedCount ||
  report.numFailedTests !== 0
) {
  throw new Error(
    `expected ${expectedCount} selected image-manager tests to pass; ` +
      `got ${String(report.numPassedTests)} passed and ` +
      `${String(report.numFailedTests)} failed`
  );
}
NODE

echo "Gate E deterministic resource-counter verification passed."
