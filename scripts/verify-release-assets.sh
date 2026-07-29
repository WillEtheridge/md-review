#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIRECTORY="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd)"
DEFAULT_SKILL_ENTRY="${PROJECT_DIRECTORY}/internal/skillassets/mdreview/SKILL.md"
FRONTEND_DIRECTORY="${MDREVIEW_RELEASE_FRONTEND_DIRECTORY:-${PROJECT_DIRECTORY}/web/dist}"
FRONTEND_ENTRY="${FRONTEND_DIRECTORY}/index.html"
SKILL_ENTRY="${MDREVIEW_RELEASE_SKILL_ENTRY:-${DEFAULT_SKILL_ENTRY}}"
FONT_DIRECTORY="${MDREVIEW_RELEASE_FONT_DIRECTORY:-${FRONTEND_DIRECTORY}/fonts}"

if [[ ! -f "${FRONTEND_ENTRY}" ]]; then
  echo "release assets are incomplete: web/dist/index.html is missing" >&2
  exit 1
fi

SCRIPT_SOURCE="$(
  sed -nE \
    's@.*<script[^>]*src="([^"]+\.js)"[^>]*>.*@\1@p' \
    "${FRONTEND_ENTRY}" |
    head -n 1
)"

if [[ -z "${SCRIPT_SOURCE}" ]]; then
  echo "release assets are incomplete: index.html has no JavaScript entry" >&2
  exit 1
fi

if [[ ! "${SCRIPT_SOURCE}" =~ ^/?[A-Za-z0-9._/-]+\.js$ ]] ||
  [[ "${SCRIPT_SOURCE}" == //* ]]; then
  echo "release assets are incomplete: index.html has an unsafe JavaScript entry" >&2
  exit 1
fi

SCRIPT_RELATIVE_PATH="${SCRIPT_SOURCE#/}"
if [[ "/${SCRIPT_RELATIVE_PATH}/" == */../* ]] ||
  [[ ! -f "${FRONTEND_DIRECTORY}/${SCRIPT_RELATIVE_PATH}" ]]; then
  echo "release assets are incomplete: the referenced JavaScript entry is missing" >&2
  exit 1
fi

node - "${FRONTEND_DIRECTORY}" "${FRONTEND_ENTRY}" <<'NODE'
const { lstatSync, readFileSync, realpathSync, statSync } = require("node:fs");
const { dirname, relative, resolve, sep } = require("node:path");

const frontendDirectory = realpathSync(process.argv[2]);
const frontendEntry = process.argv[3];
const html = readFileSync(frontendEntry, "utf8").replace(/<!--[\s\S]*?-->/gu, "");
const scannedStylesheets = new Set();
const urlAttributes = new Set([
  "action",
  "data",
  "formaction",
  "href",
  "poster",
  "src"
]);

function fail(message) {
  process.stderr.write(`release assets are incomplete: ${message}\n`);
  process.exit(1);
}

function validateReference(
  rawReference,
  label,
  baseDirectory = frontendDirectory,
  allowNavigationSegments = false
) {
  const reference = rawReference.trim();
  if (reference.length === 0) {
    fail(`${label} is empty`);
  }
  // The release output currently needs no encoded URL attributes. Rejecting
  // them closes browser decoding differences without adding an HTML parser.
  if (reference.includes("&") || reference.includes("%")) {
    fail(`${label} uses an encoded URL`);
  }
  if (
    reference.startsWith("//") ||
    /^[A-Za-z][A-Za-z0-9+.-]*:/u.test(reference)
  ) {
    fail(`${label} is remote or uses an unsafe scheme`);
  }

  const suffixOffset = reference.search(/[?#]/u);
  const path = suffixOffset < 0 ? reference : reference.slice(0, suffixOffset);
  if (path.length === 0 || !/^\/?[A-Za-z0-9._/-]+$/u.test(path)) {
    fail(`${label} is not a safe local path`);
  }
  const relativePath = path.startsWith("/") ? path.slice(1) : path;
  const segments = relativePath.split("/");
  if (
    segments.some(
      (segment) =>
        segment.length === 0 ||
        (!allowNavigationSegments && (segment === "." || segment === ".."))
    )
  ) {
    fail(`${label} traverses or has an ambiguous path`);
  }

  const candidate = resolve(
    path.startsWith("/") ? frontendDirectory : baseDirectory,
    relativePath
  );
  let candidateRealPath;
  try {
    if (lstatSync(candidate).isSymbolicLink()) {
      fail(`${label} references a symbolic link`);
    }
    candidateRealPath = realpathSync(candidate);
  } catch {
    fail(`${label} references a missing local asset`);
  }
  const relativeRealPath = relative(frontendDirectory, candidateRealPath);
  if (
    relativeRealPath === ".." ||
    relativeRealPath.startsWith(`..${sep}`) ||
    resolve(frontendDirectory, relativeRealPath) !== candidateRealPath
  ) {
    fail(`${label} escapes the frontend directory`);
  }
  if (!statSync(candidateRealPath).isFile()) {
    fail(`${label} does not reference a regular file`);
  }
  return candidateRealPath;
}

function scanCSS(css, baseDirectory, label) {
  const source = css.replace(/\/\*[\s\S]*?\*\//gu, "");
  const importPattern =
    /@import\s+(?:url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]+))\s*\)|"([^"]*)"|'([^']*)')[^;]*;/giu;
  const importedStylesheets = [];
  let importMatch;
  while ((importMatch = importPattern.exec(source)) !== null) {
    const reference =
      importMatch[1] ??
      importMatch[2] ??
      importMatch[3] ??
      importMatch[4] ??
      importMatch[5];
    importedStylesheets.push(
      validateReference(
        reference,
        `${label} @import`,
        baseDirectory,
        true
      )
    );
  }
  importPattern.lastIndex = 0;
  if (/@import\b/iu.test(source.replace(importPattern, ""))) {
    fail(`${label} has an unsupported or malformed @import`);
  }

  const urlPattern =
    /url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]+))\s*\)/giu;
  let urlMatch;
  while ((urlMatch = urlPattern.exec(source)) !== null) {
    validateReference(
      urlMatch[1] ?? urlMatch[2] ?? urlMatch[3],
      `${label} url()`,
      baseDirectory,
      true
    );
  }
  urlPattern.lastIndex = 0;
  if (/url\s*\(/iu.test(source.replace(urlPattern, ""))) {
    fail(`${label} has an unsupported or malformed url()`);
  }

  for (const stylesheet of importedStylesheets) {
    scanStylesheet(stylesheet, `${label} imported stylesheet`);
  }
}

function scanStylesheet(stylesheet, label) {
  if (scannedStylesheets.has(stylesheet)) {
    return;
  }
  scannedStylesheets.add(stylesheet);
  scanCSS(readFileSync(stylesheet, "utf8"), dirname(stylesheet), label);
}

const tagPattern = /<([A-Za-z][A-Za-z0-9:-]*)\b((?:"[^"]*"|'[^']*'|[^'">])*)>/gu;
const attributePattern =
  /(?:^|\s)([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/gu;
let styleElementCount = 0;
let tagMatch;
while ((tagMatch = tagPattern.exec(html)) !== null) {
  const tagName = tagMatch[1].toLowerCase();
  if (tagName === "base") {
    fail("<base> elements are not allowed");
  }
  if (tagName === "style") {
    styleElementCount += 1;
  }

  const attributes = new Map();
  const resolvedReferences = new Map();
  let attributeMatch;
  attributePattern.lastIndex = 0;
  while ((attributeMatch = attributePattern.exec(tagMatch[2])) !== null) {
    const name = attributeMatch[1].toLowerCase();
    const value = attributeMatch[2] ?? attributeMatch[3] ?? attributeMatch[4];
    if (value === undefined) {
      continue;
    }
    if (attributes.has(name)) {
      fail(`<${tagName}> has duplicate ${name} attributes`);
    }
    attributes.set(name, value);
  }

  for (const name of urlAttributes) {
    const value = attributes.get(name);
    if (value !== undefined) {
      resolvedReferences.set(
        name,
        validateReference(value, `<${tagName}> ${name}`)
      );
    }
  }

  const sourceSet = attributes.get("srcset");
  if (sourceSet !== undefined) {
    const candidates = sourceSet.split(",");
    if (candidates.length === 0) {
      fail(`<${tagName}> srcset is empty`);
    }
    for (const [index, candidate] of candidates.entries()) {
      const reference = candidate.trim().split(/\s+/u)[0];
      validateReference(reference, `<${tagName}> srcset candidate ${String(index + 1)}`);
    }
  }

  if (
    tagName === "meta" &&
    attributes.get("http-equiv")?.toLowerCase() === "refresh"
  ) {
    const refresh = attributes.get("content") ?? "";
    const refreshMatch = /^\s*\d+(?:\.\d+)?\s*;\s*url\s*=\s*(.+)\s*$/iu.exec(refresh);
    if (!refreshMatch) {
      fail("<meta> refresh has an invalid content value");
    }
    validateReference(refreshMatch[1], "<meta> refresh URL");
  }

  const style = attributes.get("style");
  if (style !== undefined) {
    scanCSS(style, frontendDirectory, `<${tagName}> style`);
  }

  if (
    tagName === "link" &&
    attributes
      .get("rel")
      ?.split(/\s+/u)
      .some((token) => token.toLowerCase() === "stylesheet")
  ) {
    const stylesheet = resolvedReferences.get("href");
    if (stylesheet === undefined) {
      fail('<link rel="stylesheet"> has no href');
    }
    scanStylesheet(stylesheet, "<link> stylesheet");
  }
}

const styleElementPattern =
  /<style\b(?:"[^"]*"|'[^']*'|[^'">])*>([\s\S]*?)<\/style\s*>/giu;
let scannedStyleElementCount = 0;
let styleElementMatch;
while ((styleElementMatch = styleElementPattern.exec(html)) !== null) {
  scannedStyleElementCount += 1;
  scanCSS(
    styleElementMatch[1],
    frontendDirectory,
    `<style> element ${String(scannedStyleElementCount)}`
  );
}
if (scannedStyleElementCount !== styleElementCount) {
  fail("an inline <style> element could not be parsed safely");
}
NODE

node "${PROJECT_DIRECTORY}/web/scripts/verify-fonts.mjs" "${FONT_DIRECTORY}"

if grep -R -q 'MDREVIEW_PLACEHOLDER' "${FRONTEND_DIRECTORY}"; then
  echo "release assets are incomplete: the frontend is still a placeholder" >&2
  exit 1
fi

if [[ ! -f "${SKILL_ENTRY}" ]]; then
  echo "release assets are incomplete: the Agent Skill is missing" >&2
  exit 1
fi

if grep -q 'MDREVIEW_PLACEHOLDER' "${SKILL_ENTRY}"; then
  echo "release assets are incomplete: the Agent Skill is still a placeholder" >&2
  exit 1
fi
