#!/usr/bin/env node

import { gunzipSync } from "node:zlib";
import { readFileSync } from "node:fs";
import { posix } from "node:path";
import { pathToFileURL } from "node:url";

import { parseSourceManifest, sourceManifestPath } from "./source-manifest.mjs";

const epoch = 1_785_283_200;
const releaseFiles = [
  ["mdreview", 0o755],
  ["README.md", 0o644],
  ["SECURITY.md", 0o644],
  ["THIRD_PARTY_NOTICES.md", 0o644],
  ["LICENSE", 0o644],
  ["THIRD_PARTY_NOTICES.txt", 0o644],
  ["mdreview.spdx.json", 0o644],
  ["schema/review-v1.schema.json", 0o644]
];

function fail(message) {
  throw new Error(message);
}

function text(block, offset, length) {
  const end = block.indexOf(0, offset);
  return block
    .subarray(offset, end < 0 || end > offset + length ? offset + length : end)
    .toString();
}

function octal(block, offset, length, label) {
  const value = text(block, offset, length).trim();
  if (!/^[0-7]+$/u.test(value)) {
    fail(`invalid tar ${label}: ${value}`);
  }
  return Number.parseInt(value, 8);
}

function entries(archivePath) {
  const gzip = readFileSync(archivePath);
  if (gzip.length < 10 || gzip[0] !== 0x1f || gzip[1] !== 0x8b) {
    fail("archive is not gzip");
  }
  if (gzip.readUInt32LE(4) !== 0 || (gzip[3] & 0x08) !== 0) {
    fail("gzip header contains a timestamp or original filename");
  }
  const tar = gunzipSync(gzip);
  const result = [];
  for (let offset = 0; offset + 512 <= tar.length;) {
    const block = tar.subarray(offset, offset + 512);
    if (block.every((byte) => byte === 0)) {
      break;
    }
    const name = text(block, 0, 100);
    const prefix = text(block, 345, 155);
    const path = prefix === "" ? name : `${prefix}/${name}`;
    const size = octal(block, 124, 12, "size");
    const storedChecksum = octal(block, 148, 8, "checksum");
    const checksumBlock = Buffer.from(block);
    checksumBlock.fill(0x20, 148, 156);
    const calculatedChecksum = checksumBlock.reduce((sum, byte) => sum + byte, 0);
    if (storedChecksum !== calculatedChecksum) {
      fail(`tar checksum differs: ${path}`);
    }
    result.push({
      path,
      mode: octal(block, 100, 8, "mode"),
      uid: octal(block, 108, 8, "uid"),
      gid: octal(block, 116, 8, "gid"),
      mtime: octal(block, 136, 12, "mtime"),
      type: String.fromCharCode(block[156]),
      data: tar.subarray(offset + 512, offset + 512 + size)
    });
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  return result;
}

function verifyCommon(archiveEntries) {
  const paths = archiveEntries.map((entry) => entry.path);
  const sorted = [...paths].sort((left, right) =>
    Buffer.compare(Buffer.from(left), Buffer.from(right))
  );
  if (paths.join("\n") !== sorted.join("\n")) {
    fail("archive entries are not bytewise sorted");
  }
  if (new Set(paths).size !== paths.length) {
    fail("archive contains duplicate paths");
  }
  for (const entry of archiveEntries) {
    if (entry.uid !== 0 || entry.gid !== 0 || entry.mtime !== epoch) {
      fail(`archive metadata differs from the frozen contract: ${entry.path}`);
    }
    if (entry.path.startsWith("/") || entry.path.split("/").includes("..")) {
      fail(`archive path is unsafe: ${entry.path}`);
    }
  }
}

function verifyRelease(archiveEntries, root) {
  const expectedFiles = new Map(releaseFiles.map(([path, mode]) => [posix.join(root, path), mode]));
  const expectedDirectories = new Set([`${root}/`]);
  for (const [path] of releaseFiles) {
    let parent = posix.dirname(path);
    while (parent !== ".") {
      expectedDirectories.add(`${posix.join(root, parent)}/`);
      parent = posix.dirname(parent);
    }
  }
  const expectedPaths = new Set([...expectedDirectories, ...expectedFiles.keys()]);
  for (const entry of archiveEntries) {
    if (!expectedPaths.delete(entry.path)) {
      fail(`binary archive contains an unexpected entry: ${entry.path}`);
    }
    if (expectedDirectories.has(entry.path)) {
      if (entry.type !== "5" || entry.mode !== 0o755 || entry.data.length !== 0) {
        fail(`binary archive directory metadata differs: ${entry.path}`);
      }
    } else if (entry.type !== "0" || entry.mode !== expectedFiles.get(entry.path)) {
      fail(`binary archive file mode or type differs: ${entry.path}`);
    }
  }
  if (expectedPaths.size !== 0) {
    fail(`binary archive is missing entries: ${[...expectedPaths].join(", ")}`);
  }
  const marker = Buffer.from("MDREVIEW_PLACEHOLDER");
  if (archiveEntries.some((entry) => entry.data.includes(marker))) {
    fail("binary archive contains a release placeholder");
  }
}

function verifySource(archiveEntries, root) {
  const prefix = `${root}/`;
  const manifestEntry = archiveEntries.find(
    (entry) => entry.path === `${prefix}${sourceManifestPath}`
  );
  if (manifestEntry === undefined || manifestEntry.type !== "0") {
    fail("source archive is missing its source manifest");
  }
  const manifest = parseSourceManifest(manifestEntry.data.toString("utf8"));
  const expectedFiles = new Map(manifest.map((entry) => [`${prefix}${entry.path}`, entry.mode]));
  const expectedDirectories = new Set([prefix]);
  for (const entry of manifest) {
    let parent = posix.dirname(entry.path);
    while (parent !== ".") {
      expectedDirectories.add(`${prefix}${parent}/`);
      parent = posix.dirname(parent);
    }
  }
  const expectedPaths = new Set([...expectedDirectories, ...expectedFiles.keys()]);
  for (const entry of archiveEntries) {
    if (!expectedPaths.delete(entry.path)) {
      fail(`source archive contains an entry absent from its manifest: ${entry.path}`);
    }
    if (expectedDirectories.has(entry.path)) {
      if (entry.type !== "5" || entry.mode !== 0o755 || entry.data.length !== 0) {
        fail(`source archive directory metadata differs: ${entry.path}`);
      }
    } else if (entry.type !== "0" || entry.mode !== expectedFiles.get(entry.path)) {
      fail(`source archive file mode or type differs from its manifest: ${entry.path}`);
    }
  }
  if (expectedPaths.size !== 0) {
    fail(`source archive is missing manifest entries: ${[...expectedPaths].join(", ")}`);
  }
}

export function verifyArchive(kind, archivePath, root) {
  const archiveEntries = entries(archivePath);
  verifyCommon(archiveEntries);
  if (kind === "release") {
    verifyRelease(archiveEntries, root);
  } else if (kind === "source") {
    verifySource(archiveEntries, root);
  } else {
    fail(`unknown archive kind: ${kind}`);
  }
}

function main() {
  const [, , kind, archivePath, root] = process.argv;
  if (root === undefined) {
    fail("usage: verify-archive.mjs source|release ARCHIVE ROOT");
  }
  verifyArchive(kind, archivePath, root);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`verify release archive: ${message}\n`);
    process.exitCode = 1;
  }
}
