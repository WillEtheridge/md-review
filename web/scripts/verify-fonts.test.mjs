import { Buffer } from "node:buffer";
import { createHash } from "node:crypto";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import assert from "node:assert/strict";

import { verifyFontDirectory } from "./verify-fonts.mjs";

const families = [
  {
    family: "PT Serif",
    release: "main@7ff85c87f93ea6cca5f41c69f2e4edcb90240f26",
    directory: "pt-serif",
    license: "OFL.txt",
    files: [
      ["PT_Serif-Web-Regular.ttf", "normal", 400],
      ["PT_Serif-Web-Italic.ttf", "italic", 400],
      ["PT_Serif-Web-Bold.ttf", "normal", 700],
      ["PT_Serif-Web-BoldItalic.ttf", "italic", 700]
    ]
  },
  {
    family: "Inter",
    release: "v4.1",
    directory: "inter",
    license: "LICENSE.txt",
    files: [
      ["InterVariable.woff2", "normal", "100 900"],
      ["InterVariable-Italic.woff2", "italic", "100 900"]
    ]
  },
  {
    family: "JetBrains Mono",
    release: "v2.304",
    directory: "jetbrains-mono",
    license: "OFL.txt",
    files: [
      ["JetBrainsMono-Regular.woff2", "normal", 400],
      ["JetBrainsMono-Italic.woff2", "italic", 400],
      ["JetBrainsMono-Bold.woff2", "normal", 700],
      ["JetBrainsMono-BoldItalic.woff2", "italic", 700]
    ]
  }
];

function hash(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

async function writeFixture(mutate) {
  const root = await mkdtemp(join(tmpdir(), "mdreview-font-test-"));
  const manifest = {
    schemaVersion: 1,
    families: []
  };
  await writeFile(join(root, "README.md"), "Bundled fonts.\n");
  for (const family of families) {
    const directory = join(root, family.directory);
    await mkdir(directory, { recursive: true });
    const licenseBytes = Buffer.from("SIL OPEN FONT LICENSE Version 1.1\n");
    await writeFile(join(directory, family.license), licenseBytes);
    const files = [];
    for (const [name, style, weight] of family.files) {
      const signature = name.endsWith(".ttf") ? Buffer.from([0, 1, 0, 0]) : Buffer.from("wOF2");
      const bytes = Buffer.concat([signature, Buffer.from(`${family.family}:${name}`)]);
      await writeFile(join(directory, name), bytes);
      files.push({
        path: `${family.directory}/${name}`,
        sha256: hash(bytes),
        style,
        weight
      });
    }
    manifest.families.push({
      family: family.family,
      release: family.release,
      commit: "1".repeat(40),
      source: "https://github.com/example/fonts",
      download: "https://github.com/example/fonts/releases/download/fonts.zip",
      modified: false,
      license: {
        path: `${family.directory}/${family.license}`,
        sha256: hash(licenseBytes)
      },
      files
    });
  }
  await mutate?.({ root, manifest });
  await writeFile(join(root, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return root;
}

async function withFixture(mutate, callback) {
  const root = await writeFixture(mutate);
  try {
    await callback(root);
  } finally {
    await rm(root, { force: true, recursive: true });
  }
}

test("accepts the exact complete font inventory", async () => {
  await withFixture(undefined, async (root) => {
    await verifyFontDirectory(root);
  });
});

test("rejects a missing family", async () => {
  await withFixture(
    ({ manifest }) => {
      manifest.families.pop();
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /exactly three families/u);
    }
  );
});

test("rejects unsafe manifest paths", async () => {
  await withFixture(
    ({ manifest }) => {
      manifest.families[0].files[0].path = "../font.woff2";
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /safe normalized relative path/u);
    }
  );
});

test("rejects a bad font signature", async () => {
  await withFixture(
    async ({ root, manifest }) => {
      const font = manifest.families[0].files[0];
      const bytes = Buffer.from("bad font");
      await writeFile(join(root, font.path), bytes);
      font.sha256 = hash(bytes);
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /not a WOFF2 or TrueType font/u);
    }
  );
});

test("rejects a mismatched font hash", async () => {
  await withFixture(
    ({ manifest }) => {
      manifest.families[0].files[0].sha256 = "0".repeat(64);
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /hash does not match/u);
    }
  );
});

test("rejects a placeholder or missing OFL license", async () => {
  await withFixture(
    async ({ root, manifest }) => {
      const license = manifest.families[0].license;
      const bytes = Buffer.from("MDREVIEW_PLACEHOLDER\n");
      await writeFile(join(root, license.path), bytes);
      license.sha256 = hash(bytes);
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /missing the OFL 1.1 text/u);
    }
  );
});

test("rejects an unmanifested font file", async () => {
  await withFixture(
    async ({ root }) => {
      const path = join(root, "inter", "extra.woff2");
      await mkdir(dirname(path), { recursive: true });
      await writeFile(path, "wOF2extra");
    },
    async (root) => {
      await assert.rejects(verifyFontDirectory(root), /missing or unmanifested files/u);
    }
  );
});
