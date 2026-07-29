#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

function fail(message) {
  throw new Error(message);
}

function object(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    fail(`${label} must be an object`);
  }
  return value;
}

export function verifySPDX(path) {
  const document = object(JSON.parse(readFileSync(path, "utf8")), "SPDX document");
  if (document.spdxVersion !== "SPDX-2.3" || document.dataLicense !== "CC0-1.0") {
    fail("SPDX document does not use the frozen SPDX 2.3 envelope");
  }
  if (object(document.creationInfo, "SPDX creation info").created !== "2026-07-29T00:00:00Z") {
    fail("SPDX document creation time differs from the frozen source epoch");
  }
  if (!Array.isArray(document.packages) || !Array.isArray(document.files)) {
    fail("SPDX document must contain package and file inventories");
  }
  const application = document.packages.find(
    (package_) => object(package_, "SPDX package").SPDXID === "SPDXRef-Package-mdReview"
  );
  if (
    application === undefined ||
    application.name !== "mdReview" ||
    application.versionInfo !== "v0.1.0" ||
    application.licenseDeclared !== "MIT" ||
    application.licenseConcluded !== "MIT"
  ) {
    fail("SPDX document does not contain the frozen MIT mdReview package");
  }

  const elements = new Set(["SPDXRef-DOCUMENT"]);
  for (const rawElement of [...document.packages, ...document.files]) {
    const element = object(rawElement, "SPDX element");
    if (typeof element.SPDXID !== "string" || !element.SPDXID.startsWith("SPDXRef-")) {
      fail("SPDX element has an invalid SPDXID");
    }
    if (elements.has(element.SPDXID)) {
      fail(`SPDX document contains a duplicate SPDXID: ${element.SPDXID}`);
    }
    elements.add(element.SPDXID);
  }
  if (!Array.isArray(document.relationships) || document.relationships.length === 0) {
    fail("SPDX document contains no relationships");
  }
  for (const rawRelationship of document.relationships) {
    const relationship = object(rawRelationship, "SPDX relationship");
    if (!elements.has(relationship.spdxElementId)) {
      fail(`SPDX relationship source does not exist: ${relationship.spdxElementId}`);
    }
    if (!elements.has(relationship.relatedSpdxElement)) {
      fail(`SPDX relationship target does not exist: ${relationship.relatedSpdxElement}`);
    }
  }
  const buildTools = document.packages.filter(
    (package_) => package_.primaryPackagePurpose === "BUILD_TOOL"
  );
  if (buildTools.length === 0) {
    fail("SPDX document does not distinguish frontend build dependencies");
  }
  for (const buildTool of buildTools) {
    if (
      !document.relationships.some(
        (relationship) =>
          relationship.spdxElementId === buildTool.SPDXID &&
          relationship.relationshipType === "BUILD_DEPENDENCY_OF" &&
          relationship.relatedSpdxElement === application.SPDXID
      )
    ) {
      fail(`SPDX build tool has no BUILD_DEPENDENCY_OF relationship: ${buildTool.SPDXID}`);
    }
  }
}

function main() {
  const [, , path] = process.argv;
  if (path === undefined) {
    fail("usage: verify-spdx.mjs SPDX_PATH");
  }
  verifySPDX(path);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`verify release SPDX: ${message}\n`);
    process.exitCode = 1;
  }
}
