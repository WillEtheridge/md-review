#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"

"${SCRIPT_DIRECTORY}/build-dev.sh"
npm --prefix "${PROJECT_DIRECTORY}/web" run test:go-server
