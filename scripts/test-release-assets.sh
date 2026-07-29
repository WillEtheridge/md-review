#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
VERIFY_SCRIPT="${SCRIPT_DIRECTORY}/verify-release-assets.sh"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
TEST_DIRECTORY="$(mktemp -d)"
FRONTEND_DIRECTORY="${TEST_DIRECTORY}/frontend"
SKILL_ENTRY="${TEST_DIRECTORY}/SKILL.md"
FONT_DIRECTORY="${PROJECT_DIRECTORY}/web/public/fonts"

trap 'rm -rf -- "${TEST_DIRECTORY}"' EXIT

mkdir -p "${FRONTEND_DIRECTORY}"

verify_fixture() {
  MDREVIEW_RELEASE_FRONTEND_DIRECTORY="${FRONTEND_DIRECTORY}" \
    MDREVIEW_RELEASE_SKILL_ENTRY="${SKILL_ENTRY}" \
    MDREVIEW_RELEASE_FONT_DIRECTORY="${FONT_DIRECTORY}" \
    "${VERIFY_SCRIPT}"
}

expect_rejection() {
  local case_name="$1"
  if verify_fixture >/dev/null 2>&1; then
    echo "release guard accepted invalid fixture: ${case_name}" >&2
    exit 1
  fi
}

write_index_with_reference() {
  local reference="$1"
  printf '%s\n%s\n' \
    '<script type="module" src="/assets/application.js"></script>' \
    "${reference}" >"${FRONTEND_DIRECTORY}/index.html"
}

write_valid_stylesheets() {
  mkdir -p "${FRONTEND_DIRECTORY}/assets/styles"
  printf '%s\n' \
    '@import "./styles/theme.css";' \
    '.application { background-image: url("application.png"); }' \
    >"${FRONTEND_DIRECTORY}/assets/application.css"
  printf '%s\n' \
    '.theme { background-image: url("../application.png"); }' \
    >"${FRONTEND_DIRECTORY}/assets/styles/theme.css"
}

expect_rejection "missing frontend and skill"

printf '<!doctype html>\n' >"${FRONTEND_DIRECTORY}/index.html"
printf 'valid skill\n' >"${SKILL_ENTRY}"
printf 'unreferenced application\n' >"${FRONTEND_DIRECTORY}/unreferenced.js"
expect_rejection "unreferenced frontend JavaScript"

printf '<script type="module" src="https://example.com/application.js"></script>\n' \
  >"${FRONTEND_DIRECTORY}/index.html"
expect_rejection "remote frontend JavaScript"

printf '<script type="module" src="/assets/../application.js"></script>\n' \
  >"${FRONTEND_DIRECTORY}/index.html"
expect_rejection "traversing frontend JavaScript"

printf '<script type="module" src="/assets/application.js"></script>\n' \
  >"${FRONTEND_DIRECTORY}/index.html"
expect_rejection "missing referenced frontend JavaScript"

mkdir -p "${FRONTEND_DIRECTORY}/assets"
printf 'valid application\n' >"${FRONTEND_DIRECTORY}/assets/application.js"
printf 'valid icon\n' >"${FRONTEND_DIRECTORY}/assets/application.ico"
printf 'valid image\n' >"${FRONTEND_DIRECTORY}/assets/application.png"
write_valid_stylesheets

write_index_with_reference '<base>'
expect_rejection "HTML base element"

write_index_with_reference \
  '<link rel="stylesheet" href="https://example.com/application.css">'
expect_rejection "remote frontend stylesheet"

write_index_with_reference \
  "<link rel='preload' href='https://example.com/application.js' as='script'>"
expect_rejection "remote frontend preload"

write_index_with_reference \
  '<link rel="icon" href="//example.com/application.ico">'
expect_rejection "protocol-relative frontend icon"

write_index_with_reference \
  '<link rel="stylesheet" href="/assets/../application.css">'
expect_rejection "traversing frontend stylesheet"

write_index_with_reference \
  '<link rel="stylesheet" href="/assets/missing.css">'
expect_rejection "missing local frontend stylesheet"

write_index_with_reference \
  '<iframe src="https://example.com/runtime.html"></iframe>'
expect_rejection "remote iframe source"

write_index_with_reference \
  '<img src="/assets/application.png" srcset="/assets/application.png 1x, https://example.com/application.png 2x">'
expect_rejection "remote image source set"

printf '@import "https://example.com/theme.css";\n' \
  >"${FRONTEND_DIRECTORY}/assets/application.css"
write_index_with_reference \
  '<link rel="stylesheet" href="/assets/application.css">'
expect_rejection "remote CSS import"

printf '.application { background: url(https://example.com/image.png); }\n' \
  >"${FRONTEND_DIRECTORY}/assets/application.css"
write_index_with_reference \
  '<link rel="stylesheet" href="/assets/application.css">'
expect_rejection "remote linked CSS URL"

printf 'outside frontend\n' >"${TEST_DIRECTORY}/outside.png"
printf '.application { background: url("../../outside.png"); }\n' \
  >"${FRONTEND_DIRECTORY}/assets/application.css"
write_index_with_reference \
  '<link rel="stylesheet" href="/assets/application.css">'
expect_rejection "escaping linked CSS URL"

printf '@import "./styles/theme.css";\n' \
  >"${FRONTEND_DIRECTORY}/assets/application.css"
printf '.theme { background: url("//example.com/image.png"); }\n' \
  >"${FRONTEND_DIRECTORY}/assets/styles/theme.css"
write_index_with_reference \
  '<link rel="stylesheet" href="/assets/application.css">'
expect_rejection "remote nested CSS URL"

write_valid_stylesheets
write_index_with_reference \
  '<style>@import "https://example.com/theme.css";</style>'
expect_rejection "remote inline CSS import"

write_index_with_reference \
  '<div style="background: url(https://example.com/image.png)"></div>'
expect_rejection "remote style attribute URL"

write_valid_stylesheets
printf 'MDREVIEW_PLACEHOLDER:WEB\n' >"${FRONTEND_DIRECTORY}/assets/application.js"
write_index_with_reference \
  '<link rel="stylesheet" href="/assets/application.css">'
expect_rejection "frontend placeholder"

printf 'valid application\n' >"${FRONTEND_DIRECTORY}/assets/application.js"
printf 'MDREVIEW_PLACEHOLDER:SKILL\n' >"${SKILL_ENTRY}"
expect_rejection "Agent Skill placeholder"

printf 'valid skill\n' >"${SKILL_ENTRY}"
verify_fixture

printf '%s\n' \
  '<!doctype html>' \
  '<html lang="en">' \
  '  <head>' \
  '    <meta charset="UTF-8" />' \
  '    <meta name="viewport" content="width=device-width, initial-scale=1.0" />' \
  '    <script type="module" crossorigin src="/assets/application.js"></script>' \
  '    <link rel="stylesheet" crossorigin href="/assets/application.css">' \
  '    <style>.inline { background: url("/assets/application.png"); }</style>' \
  '  </head>' \
  '  <body><div id="app" style="background: url(/assets/application.png)"></div></body>' \
  '</html>' >"${FRONTEND_DIRECTORY}/index.html"
verify_fixture
