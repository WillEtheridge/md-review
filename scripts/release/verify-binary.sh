#!/usr/bin/env bash

set -euo pipefail

if (($# != 2)); then
  echo "usage: verify-binary.sh BINARY linux/amd64|darwin/arm64" >&2
  exit 1
fi

BINARY_PATH="$1"
TARGET="$2"
case "${TARGET}" in
linux/amd64)
  TARGET_GOOS="linux"
  TARGET_GOARCH="amd64"
  TARGET_ARCH_SETTING=$'\tbuild\tGOAMD64=v1'
  ;;
darwin/arm64)
  TARGET_GOOS="darwin"
  TARGET_GOARCH="arm64"
  TARGET_ARCH_SETTING=""
  ;;
*)
  echo "unsupported release target: ${TARGET}" >&2
  exit 1
  ;;
esac
if [[ ! -f "${BINARY_PATH}" || ! -x "${BINARY_PATH}" ]]; then
  echo "packaged binary is missing or not executable: ${BINARY_PATH}" >&2
  exit 1
fi

command -v go >/dev/null

if [[ "${TARGET}" == "linux/amd64" ]] && command -v readelf >/dev/null; then
  PROGRAM_HEADERS="$(readelf -l "${BINARY_PATH}")"
  if grep -Eq '(^|[[:space:]])INTERP([[:space:]]|$)|Requesting program interpreter' \
    <<<"${PROGRAM_HEADERS}"; then
    echo "packaged binary has an ELF interpreter" >&2
    exit 1
  fi

  DYNAMIC_SECTION="$(readelf -d "${BINARY_PATH}")"
  if grep -q '(NEEDED)' <<<"${DYNAMIC_SECTION}"; then
    echo "packaged binary has dynamic-library dependencies" >&2
    exit 1
  fi
fi

BUILD_INFO="$(go version -m "${BINARY_PATH}")"
for expected_setting in \
  $'\tbuild\tCGO_ENABLED=0' \
  $'\tbuild\tGOOS='"${TARGET_GOOS}" \
  $'\tbuild\tGOARCH='"${TARGET_GOARCH}"; do
  if ! grep -Fqx "${expected_setting}" <<<"${BUILD_INFO}"; then
    echo "packaged binary is missing build setting: ${expected_setting##*$'\t'}" >&2
    exit 1
  fi
done
if [[ -n "${TARGET_ARCH_SETTING}" ]] &&
  ! grep -Fqx "${TARGET_ARCH_SETTING}" <<<"${BUILD_INFO}"; then
  echo "packaged binary is missing build setting: GOAMD64=v1" >&2
  exit 1
fi
printf '%s\n' "${BUILD_INFO}"

if grep -a -q 'MDREVIEW_PLACEHOLDER' "${BINARY_PATH}"; then
  echo "packaged binary contains a release placeholder" >&2
  exit 1
fi

HOST_TARGET="$(go env GOOS)/$(go env GOARCH)"
if [[ "${HOST_TARGET}" != "${TARGET}" ]]; then
  echo "Skipping native smoke test for ${TARGET} on ${HOST_TARGET}."
  exit 0
fi

SMOKE_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-release-smoke.XXXXXXXX")"
WORKSPACE_DIRECTORY="${SMOKE_DIRECTORY}/workspace"
RUNTIME_DIRECTORY="${SMOKE_DIRECTORY}/runtime"
USER_DIRECTORY="${SMOKE_DIRECTORY}/home"
OUTPUT_PATH="${SMOKE_DIRECTORY}/output"
PROCESS_ID=""
cleanup() {
  if [[ -n "${PROCESS_ID}" ]] && kill -0 "${PROCESS_ID}" 2>/dev/null; then
    kill -TERM "${PROCESS_ID}" 2>/dev/null || true
    wait "${PROCESS_ID}" 2>/dev/null || true
  fi
  rm -rf -- "${SMOKE_DIRECTORY}"
}
trap cleanup EXIT

mkdir -m 0700 -p "${WORKSPACE_DIRECTORY}" "${RUNTIME_DIRECTORY}" "${USER_DIRECTORY}"
printf '# Packaged release smoke\n' >"${WORKSPACE_DIRECTORY}/release.md"

env -i \
  PATH=/nonexistent \
  HOME="${USER_DIRECTORY}" \
  XDG_RUNTIME_DIR="${RUNTIME_DIRECTORY}" \
  "${BINARY_PATH}" "${WORKSPACE_DIRECTORY}" >"${OUTPUT_PATH}" 2>&1 &
PROCESS_ID=$!

ready=false
for _ in {1..100}; do
  if grep -q 'Waiting for a browser connection' "${OUTPUT_PATH}" 2>/dev/null; then
    ready=true
    break
  fi
  if ! kill -0 "${PROCESS_ID}" 2>/dev/null; then
    break
  fi
  sleep 0.05
done
if [[ "${ready}" != "true" ]]; then
  echo "packaged binary did not reach its readiness barrier without Node.js" >&2
  cat "${OUTPUT_PATH}" >&2
  exit 1
fi

kill -TERM "${PROCESS_ID}"
wait "${PROCESS_ID}"
PROCESS_ID=""
