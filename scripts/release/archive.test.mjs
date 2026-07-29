import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { promisify } from "node:util";
import { gunzipSync } from "node:zlib";

import { createArchive } from "./archive.mjs";
import { sourceManifestPath } from "./source-manifest.mjs";
import { verifyArchive } from "./verify-archive.mjs";

const version = "v0.1.0";
const sourceRoot = `mdreview-${version}-source`;
const releaseRoot = `mdreview-${version}-linux-amd64`;
const execFileAsync = promisify(execFile);
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

async function writeFixture(root, path, contents = `${path}\n`) {
  const absolutePath = join(root, ...path.split("/"));
  await mkdir(join(absolutePath, ".."), { recursive: true });
  await writeFile(absolutePath, contents);
}

test("source archives are deterministic and exclude generated and private files", async () => {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "mdreview-source-archive-test."));
  try {
    const source = join(temporaryDirectory, "source");
    await writeFixture(source, "go.mod", "module example.test/release\n");
    await writeFixture(source, ".private/release-record.md", "local release evidence\n");
    await writeFixture(source, "web/dist/placeholder.txt", "MDREVIEW_PLACEHOLDER:WEB\n");
    await writeFixture(source, "web/dist/generated.js", "generated\n");
    await writeFixture(source, "web/node_modules/example/LICENSE", "not source\n");
    await writeFixture(source, "build/mdreview", "not source\n");
    await writeFixture(source, ".env", "PRIVATE_TOKEN=must-not-ship\n");
    await writeFixture(source, "private-key.pem", "must-not-ship\n");
    await writeFixture(source, "go.mod~", "editor backup\n");
    await writeFixture(
      source,
      sourceManifestPath,
      ["0644 go.mod", `0644 ${sourceManifestPath}`, "0644 web/dist/placeholder.txt", ""].join("\n")
    );

    const first = join(temporaryDirectory, "first.tar.gz");
    const second = join(temporaryDirectory, "second.tar.gz");
    createArchive("source", source, first, sourceRoot);
    createArchive("source", source, second, sourceRoot);

    assert.deepEqual(await readFile(first), await readFile(second));
    verifyArchive("source", first, sourceRoot);
    const extracted = join(temporaryDirectory, "extracted");
    await mkdir(extracted);
    await execFileAsync("tar", ["-xzf", first, "-C", extracted]);
    const regenerated = join(temporaryDirectory, "regenerated.tar.gz");
    createArchive("source", join(extracted, sourceRoot), regenerated, sourceRoot);
    verifyArchive("source", regenerated, sourceRoot);
    assert.deepEqual(await readFile(first), await readFile(regenerated));
    const tarBytes = gunzipSync(await readFile(first));
    assert.equal(tarBytes.includes(Buffer.from(".private/release-record.md")), false);
    assert.equal(tarBytes.includes(Buffer.from("local release evidence")), false);
    assert.equal(tarBytes.includes(Buffer.from("web/dist/generated.js")), false);
    assert.equal(tarBytes.includes(Buffer.from("web/node_modules")), false);
    assert.equal(tarBytes.includes(Buffer.from("PRIVATE_TOKEN=must-not-ship")), false);
    assert.equal(tarBytes.includes(Buffer.from("private-key.pem")), false);
    assert.equal(tarBytes.includes(Buffer.from("go.mod~")), false);
  } finally {
    await rm(temporaryDirectory, { force: true, recursive: true });
  }
});

test("source archive creation rejects a manifest entry missing from the clean root", async () => {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "mdreview-source-manifest-test."));
  try {
    const source = join(temporaryDirectory, "source");
    await writeFixture(source, "web/dist/placeholder.txt", "MDREVIEW_PLACEHOLDER:WEB\n");
    await writeFixture(
      source,
      sourceManifestPath,
      ["0644 web/dist/placeholder.txt", ""].join("\n")
    );
    assert.throws(
      () =>
        createArchive(
          "source",
          source,
          join(temporaryDirectory, "missing-self.tar.gz"),
          sourceRoot
        ),
      /source manifest must include itself/u
    );
    await writeFixture(
      source,
      sourceManifestPath,
      [
        "0644 missing-required-input.txt",
        `0644 ${sourceManifestPath}`,
        "0644 web/dist/placeholder.txt",
        ""
      ].join("\n")
    );
    assert.throws(
      () => createArchive("source", source, join(temporaryDirectory, "source.tar.gz"), sourceRoot),
      /missing-required-input/u
    );
    await writeFixture(source, ".private/release-record.md", "local evidence\n");
    await writeFixture(
      source,
      sourceManifestPath,
      [
        "0644 .private/release-record.md",
        `0644 ${sourceManifestPath}`,
        "0644 web/dist/placeholder.txt",
        ""
      ].join("\n")
    );
    assert.throws(
      () => createArchive("source", source, join(temporaryDirectory, "private.tar.gz"), sourceRoot),
      /forbidden source manifest path/u
    );
  } finally {
    await rm(temporaryDirectory, { force: true, recursive: true });
  }
});

test("release archive contains only the frozen files with normalized metadata", async () => {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "mdreview-binary-archive-test."));
  try {
    const staging = join(temporaryDirectory, "staging");
    for (const path of releaseFiles) {
      await writeFixture(staging, path);
    }
    const archive = join(temporaryDirectory, "release.tar.gz");
    createArchive("release", staging, archive, releaseRoot);
    verifyArchive("release", archive, releaseRoot);

    const damaged = Buffer.from(await readFile(archive));
    damaged[4] = 1;
    const damagedPath = join(temporaryDirectory, "damaged.tar.gz");
    await writeFile(damagedPath, damaged);
    assert.throws(
      () => verifyArchive("release", damagedPath, releaseRoot),
      /gzip header contains a timestamp/u
    );
  } finally {
    await rm(temporaryDirectory, { force: true, recursive: true });
  }
});
