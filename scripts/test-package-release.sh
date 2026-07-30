#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"

node --test "${SCRIPT_DIRECTORY}/release/"*.test.mjs
bash -n \
  "${SCRIPT_DIRECTORY}/build-release.sh" \
  "${SCRIPT_DIRECTORY}/package-release.sh" \
  "${SCRIPT_DIRECTORY}/release/verify-binary.sh"
node --check "${SCRIPT_DIRECTORY}/release/archive.mjs"
node --check "${SCRIPT_DIRECTORY}/release/metadata.mjs"
node --check "${SCRIPT_DIRECTORY}/release/verify-archive.mjs"
node --check "${SCRIPT_DIRECTORY}/release/verify-spdx.mjs"

TEST_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-release-script-test.XXXXXXXX")"
trap 'rm -rf -- "${TEST_DIRECTORY}"' EXIT
mkdir -p "${TEST_DIRECTORY}/tools"
printf '%s\n' \
  '#!/usr/bin/bash' \
  'if [[ "$1" == "version" && "$2" == "-m" ]]; then' \
  '  printf "fixture: go1.26.5\\n\\tbuild\\tCGO_ENABLED=0\\n\\tbuild\\tGOOS=linux\\n\\tbuild\\tGOARCH=amd64\\n\\tbuild\\tGOAMD64=v1\\n"' \
  '  exit 0' \
  'fi' \
  'if [[ "$1" == "env" && "$2" == "GOOS" ]]; then printf "linux\\n"; exit 0; fi' \
  'if [[ "$1" == "env" && "$2" == "GOARCH" ]]; then printf "amd64\\n"; exit 0; fi' \
  'exit 1' >"${TEST_DIRECTORY}/tools/go"
printf '%s\n' \
  '#!/usr/bin/bash' \
  'printf "%s\\n" "ELF fixture without dynamic metadata"' \
  'exit 0' >"${TEST_DIRECTORY}/tools/readelf"
printf '%s\n' \
  '#!/usr/bin/bash' \
  'trap "exit 0" TERM' \
  'printf "%s\\n" "Waiting for a browser connection. Press Ctrl+C to stop."' \
  'while :; do read -r -t 1 || true; done' >"${TEST_DIRECTORY}/mdreview"
chmod 0755 \
  "${TEST_DIRECTORY}/tools/go" \
  "${TEST_DIRECTORY}/tools/readelf" \
  "${TEST_DIRECTORY}/mdreview"
PATH="${TEST_DIRECTORY}/tools:${PATH}" \
  "${SCRIPT_DIRECTORY}/release/verify-binary.sh" \
  "${TEST_DIRECTORY}/mdreview" \
  linux/amd64 >/dev/null

echo "Release packaging focused tests passed."
