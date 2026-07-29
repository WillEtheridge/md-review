#!/usr/bin/env node

import { gzipSync } from "node:zlib";
import { lstatSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, posix, resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { readSourceManifest } from "./source-manifest.mjs";

const blockSize = 512;
const sourceEpoch = 1_785_283_200;
const releaseFiles = [
  "mdreview",
  "README.md",
  "SECURITY.md",
  "THIRD_PARTY_NOTICES.md",
  "LICENSE",
  "THIRD_PARTY_NOTICES.txt",
  "mdreview.spdx.json",
  "schema/review-v1.schema.json"
];

function fail(message) {
  throw new Error(message);
}

function writeText(header, offset, length, value) {
  const encoded = Buffer.from(value, "utf8");
  if (encoded.length > length) {
    fail(`tar field is too long: ${value}`);
  }
  encoded.copy(header, offset);
}

function writeOctal(header, offset, length, value) {
  const encoded = value.toString(8).padStart(length - 1, "0");
  if (encoded.length >= length) {
    fail(`tar numeric field is too large: ${value}`);
  }
  writeText(header, offset, length, `${encoded}\0`);
}

function splitTarPath(path) {
  if (Buffer.byteLength(path) <= 100) {
    return { name: path, prefix: "" };
  }

  for (let index = path.lastIndexOf("/"); index > 0; index = path.lastIndexOf("/", index - 1)) {
    const prefix = path.slice(0, index);
    const name = path.slice(index + 1);
    if (Buffer.byteLength(prefix) <= 155 && Buffer.byteLength(name) <= 100) {
      return { name, prefix };
    }
  }
  fail(`archive path does not fit the ustar format: ${path}`);
}

function tarHeader(entry) {
  const header = Buffer.alloc(blockSize);
  const { name, prefix } = splitTarPath(entry.path);

  writeText(header, 0, 100, name);
  writeOctal(header, 100, 8, entry.mode);
  writeOctal(header, 108, 8, 0);
  writeOctal(header, 116, 8, 0);
  writeOctal(header, 124, 12, entry.data.length);
  writeOctal(header, 136, 12, sourceEpoch);
  header.fill(0x20, 148, 156);
  writeText(header, 156, 1, entry.type);
  writeText(header, 257, 6, "ustar\0");
  writeText(header, 263, 2, "00");
  writeText(header, 345, 155, prefix);

  let checksum = 0;
  for (const byte of header) {
    checksum += byte;
  }
  writeText(header, 148, 8, `${checksum.toString(8).padStart(6, "0")}\0 `);
  return header;
}

function directoryEntry(path) {
  return {
    path: path.endsWith("/") ? path : `${path}/`,
    mode: 0o755,
    type: "5",
    data: Buffer.alloc(0)
  };
}

function fileEntry(root, relativePath, archiveRoot, mode) {
  const absolutePath = join(root, ...relativePath.split("/"));
  const info = lstatSync(absolutePath);
  if (!info.isFile()) {
    fail(`release input must be a regular file: ${relativePath}`);
  }
  return {
    path: posix.join(archiveRoot, relativePath),
    mode,
    type: "0",
    data: readFileSync(absolutePath)
  };
}

function collectSourceEntries(root, archiveRoot) {
  const entries = [directoryEntry(archiveRoot)];
  const directories = new Set();
  const sourceFiles = readSourceManifest(root);
  for (const sourceFile of sourceFiles) {
    let parent = posix.dirname(sourceFile.path);
    while (parent !== ".") {
      directories.add(parent);
      parent = posix.dirname(parent);
    }
  }
  for (const directory of [...directories].sort()) {
    entries.push(directoryEntry(posix.join(archiveRoot, directory)));
  }
  for (const sourceFile of sourceFiles) {
    const absolutePath = join(root, ...sourceFile.path.split("/"));
    const info = lstatSync(absolutePath);
    const actualMode = info.mode & 0o111 ? 0o755 : 0o644;
    if (actualMode !== sourceFile.mode) {
      fail(`source file mode differs from its manifest: ${sourceFile.path}`);
    }
    entries.push(fileEntry(root, sourceFile.path, archiveRoot, sourceFile.mode));
  }
  return entries;
}

function collectReleaseEntries(root, archiveRoot) {
  const entries = [directoryEntry(archiveRoot)];
  const directories = new Set();

  for (const relativePath of releaseFiles) {
    let parent = posix.dirname(relativePath);
    while (parent !== ".") {
      directories.add(parent);
      parent = posix.dirname(parent);
    }
  }
  for (const directory of [...directories].sort()) {
    entries.push(directoryEntry(posix.join(archiveRoot, directory)));
  }
  for (const relativePath of releaseFiles) {
    entries.push(
      fileEntry(root, relativePath, archiveRoot, relativePath === "mdreview" ? 0o755 : 0o644)
    );
  }
  return entries;
}

function writeArchive(entries, outputPath) {
  entries.sort((left, right) => Buffer.compare(Buffer.from(left.path), Buffer.from(right.path)));
  const parts = [];
  for (const entry of entries) {
    parts.push(tarHeader(entry), entry.data);
    const remainder = entry.data.length % blockSize;
    if (remainder !== 0) {
      parts.push(Buffer.alloc(blockSize - remainder));
    }
  }
  parts.push(Buffer.alloc(blockSize * 2));

  mkdirSync(dirname(outputPath), { recursive: true });
  writeFileSync(outputPath, gzipSync(Buffer.concat(parts), { level: 9, mtime: 0 }), {
    mode: 0o644
  });
}

export function createArchive(kind, rootPath, outputPath, archiveRoot) {
  if (!/^[A-Za-z0-9._-]+$/u.test(archiveRoot)) {
    fail(`invalid archive root: ${archiveRoot}`);
  }
  const root = resolve(rootPath);
  const output = resolve(outputPath);
  const entries =
    kind === "source"
      ? collectSourceEntries(root, archiveRoot)
      : kind === "release"
        ? collectReleaseEntries(root, archiveRoot)
        : fail(`unknown archive kind: ${kind}`);
  writeArchive(entries, output);
}

function main() {
  const [, , kind, rootPath, outputPath, archiveRoot] = process.argv;
  if (archiveRoot === undefined) {
    fail("usage: archive.mjs source|release ROOT OUTPUT ARCHIVE_ROOT");
  }
  createArchive(kind, rootPath, outputPath, archiveRoot);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`archive release assets: ${message}\n`);
    process.exitCode = 1;
  }
}
