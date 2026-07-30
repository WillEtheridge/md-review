import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { collectNpmPackages, parseGoBuildInfo } from "./metadata.mjs";

test("Go build information must describe the selected pure-Go target", () => {
  const linuxOutput = [
    "binary: go1.26.5",
    "\tdep\texample.test/module\tv1.2.3\th1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
    "\tbuild\tCGO_ENABLED=0",
    "\tbuild\tGOOS=linux",
    "\tbuild\tGOARCH=amd64",
    "\tbuild\tGOAMD64=v1"
  ].join("\n");
  assert.deepEqual(parseGoBuildInfo(linuxOutput, "linux/amd64"), [
    {
      path: "example.test/module",
      version: "v1.2.3",
      sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    }
  ]);
  assert.throws(
    () => parseGoBuildInfo(linuxOutput.replace("CGO_ENABLED=0", "CGO_ENABLED=1")),
    /CGO_ENABLED is not 0/u
  );
  const darwinOutput = linuxOutput
    .replace("GOOS=linux", "GOOS=darwin")
    .replace("GOARCH=amd64", "GOARCH=arm64")
    .replace("\n\tbuild\tGOAMD64=v1", "");
  assert.deepEqual(parseGoBuildInfo(darwinOutput, "darwin/arm64"), [
    {
      path: "example.test/module",
      version: "v1.2.3",
      sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    }
  ]);
});

test("redistributed npm packages require installed licence evidence", async () => {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "mdreview-metadata-test."));
  try {
    const web = join(temporaryDirectory, "web");
    const packageDirectory = join(web, "node_modules/example");
    await mkdir(packageDirectory, { recursive: true });
    await writeFile(
      join(web, "package-lock.json"),
      `${JSON.stringify(
        {
          lockfileVersion: 3,
          packages: {
            "": { name: "fixture", version: "0.0.0" },
            "node_modules/example": {
              version: "1.0.0",
              resolved: "https://registry.npmjs.org/example/-/example-1.0.0.tgz",
              integrity:
                "sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
              license: "MIT"
            }
          }
        },
        null,
        2
      )}\n`
    );
    await writeFile(
      join(packageDirectory, "package.json"),
      `${JSON.stringify({ name: "example", version: "1.0.0", license: "MIT" })}\n`
    );

    assert.throws(
      () => collectNpmPackages(temporaryDirectory),
      /has no attributable licence or notice file/u
    );
    await writeFile(join(packageDirectory, "LICENSE"), "MIT fixture\n");
    const packages = collectNpmPackages(temporaryDirectory);
    assert.equal(packages.length, 1);
    assert.equal(packages[0].notices[0].text, "MIT fixture\n");
  } finally {
    await rm(temporaryDirectory, { force: true, recursive: true });
  }
});
