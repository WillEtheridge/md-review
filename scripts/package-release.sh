#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
OUTPUT_DIRECTORY="${PROJECT_DIRECTORY}/build/release"
VERSION="v0.1.0"
SOURCE_ROOT="mdreview-${VERSION}-source"
RELEASE_ROOT="mdreview-${VERSION}-linux-amd64"
SOURCE_ARCHIVE_NAME="${SOURCE_ROOT}.tar.gz"
RELEASE_ARCHIVE_NAME="${RELEASE_ROOT}.tar.gz"
SPDX_NAME="${RELEASE_ROOT}.spdx.json"
TEMPORARY_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/mdreview-package.XXXXXXXX")"
trap 'rm -rf -- "${TEMPORARY_DIRECTORY}"' EXIT

export GOTOOLCHAIN=local
export GOWORK=off
export GOENV=off
export LC_ALL=C
export TZ=UTC
umask 022

command -v go >/dev/null
command -v node >/dev/null
command -v npm >/dev/null
command -v readelf >/dev/null
command -v sha256sum >/dev/null
command -v tar >/dev/null

SOURCE_ARCHIVE="${TEMPORARY_DIRECTORY}/${SOURCE_ARCHIVE_NAME}"
node \
  "${SCRIPT_DIRECTORY}/release/archive.mjs" \
  source \
  "${PROJECT_DIRECTORY}" \
  "${SOURCE_ARCHIVE}" \
  "${SOURCE_ROOT}"
node \
  "${SCRIPT_DIRECTORY}/release/verify-archive.mjs" \
  source \
  "${SOURCE_ARCHIVE}" \
  "${SOURCE_ROOT}"

build_from_source() {
  local build_name="$1"
  local extraction_directory="${TEMPORARY_DIRECTORY}/${build_name}-source"
  local source_directory="${extraction_directory}/${SOURCE_ROOT}"
  local candidate_directory="${TEMPORARY_DIRECTORY}/${build_name}-candidate"
  local artifact_directory="${TEMPORARY_DIRECTORY}/${build_name}-artifacts"
  local regenerated_source_archive="${artifact_directory}/${SOURCE_ARCHIVE_NAME}"

  mkdir -p "${extraction_directory}" "${candidate_directory}" "${artifact_directory}"
  tar -xzf "${SOURCE_ARCHIVE}" -C "${extraction_directory}"
  node \
    "${source_directory}/scripts/release/archive.mjs" \
    source \
    "${source_directory}" \
    "${regenerated_source_archive}" \
    "${SOURCE_ROOT}"
  node \
    "${source_directory}/scripts/release/verify-archive.mjs" \
    source \
    "${regenerated_source_archive}" \
    "${SOURCE_ROOT}"
  if ! cmp "${SOURCE_ARCHIVE}" "${regenerated_source_archive}"; then
    echo "clean source root did not reproduce its source archive: ${build_name}" >&2
    exit 1
  fi
  "${source_directory}/scripts/build-release.sh" "${candidate_directory}"

  node \
    "${source_directory}/scripts/release/archive.mjs" \
    release \
    "${candidate_directory}" \
    "${artifact_directory}/${RELEASE_ARCHIVE_NAME}" \
    "${RELEASE_ROOT}"
  node \
    "${source_directory}/scripts/release/verify-archive.mjs" \
    release \
    "${artifact_directory}/${RELEASE_ARCHIVE_NAME}" \
    "${RELEASE_ROOT}"
  install -m 0644 \
    "${candidate_directory}/mdreview.spdx.json" \
    "${artifact_directory}/${SPDX_NAME}"
  node \
    "${source_directory}/scripts/release/verify-spdx.mjs" \
    "${artifact_directory}/${SPDX_NAME}"

  (
    cd "${artifact_directory}"
    sha256sum \
      "${SOURCE_ARCHIVE_NAME}" \
      "${RELEASE_ARCHIVE_NAME}" \
      "${SPDX_NAME}" >SHA256SUMS
    sha256sum -c SHA256SUMS
  )

  local packaged_directory="${TEMPORARY_DIRECTORY}/${build_name}-packaged"
  mkdir -p "${packaged_directory}"
  tar -xzf "${artifact_directory}/${RELEASE_ARCHIVE_NAME}" -C "${packaged_directory}"
  cmp \
    "${packaged_directory}/${RELEASE_ROOT}/mdreview.spdx.json" \
    "${artifact_directory}/${SPDX_NAME}"
  node \
    "${source_directory}/scripts/release/verify-spdx.mjs" \
    "${packaged_directory}/${RELEASE_ROOT}/mdreview.spdx.json"
  "${source_directory}/scripts/release/verify-binary.sh" \
    "${packaged_directory}/${RELEASE_ROOT}/mdreview"
}

build_from_source first
build_from_source second

for artifact in \
  "${SOURCE_ARCHIVE_NAME}" \
  "${RELEASE_ARCHIVE_NAME}" \
  "${SPDX_NAME}" \
  SHA256SUMS; do
  if ! cmp \
    "${TEMPORARY_DIRECTORY}/first-artifacts/${artifact}" \
    "${TEMPORARY_DIRECTORY}/second-artifacts/${artifact}"; then
    echo "two clean release builds differ: ${artifact}" >&2
    exit 1
  fi
done

mkdir -p "${OUTPUT_DIRECTORY}"
for artifact in \
  "${SOURCE_ARCHIVE_NAME}" \
  "${RELEASE_ARCHIVE_NAME}" \
  "${SPDX_NAME}" \
  SHA256SUMS; do
  rm -f -- "${OUTPUT_DIRECTORY}/${artifact}"
  install -m 0644 \
    "${TEMPORARY_DIRECTORY}/first-artifacts/${artifact}" \
    "${OUTPUT_DIRECTORY}/${artifact}"
done

echo "Release artifacts written to ${OUTPUT_DIRECTORY}"
