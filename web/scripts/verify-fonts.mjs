import { createHash } from "node:crypto";
import { lstat, readFile, readdir } from "node:fs/promises";
import { isAbsolute, posix, relative, resolve, sep } from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL, URL } from "node:url";

const expectedFamilies = new Map([
  [
    "PT Serif",
    {
      release: "main@7ff85c87f93ea6cca5f41c69f2e4edcb90240f26",
      license: "pt-serif/OFL.txt",
      files: new Set([
        "pt-serif/PT_Serif-Web-Regular.ttf",
        "pt-serif/PT_Serif-Web-Italic.ttf",
        "pt-serif/PT_Serif-Web-Bold.ttf",
        "pt-serif/PT_Serif-Web-BoldItalic.ttf"
      ])
    }
  ],
  [
    "Inter",
    {
      release: "v4.1",
      license: "inter/LICENSE.txt",
      files: new Set(["inter/InterVariable.woff2", "inter/InterVariable-Italic.woff2"])
    }
  ],
  [
    "JetBrains Mono",
    {
      release: "v2.304",
      license: "jetbrains-mono/OFL.txt",
      files: new Set([
        "jetbrains-mono/JetBrainsMono-Regular.woff2",
        "jetbrains-mono/JetBrainsMono-Italic.woff2",
        "jetbrains-mono/JetBrainsMono-Bold.woff2",
        "jetbrains-mono/JetBrainsMono-BoldItalic.woff2"
      ])
    }
  ]
]);

const sha256Pattern = /^[0-9a-f]{64}$/u;
const commitPattern = /^[0-9a-f]{40}$/u;

function record(value, description) {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${description} must be an object`);
  }
  return value;
}

function string(value, description) {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${description} must be a non-empty string`);
  }
  return value;
}

function safeRelativePath(value, description) {
  const candidate = string(value, description);
  if (
    isAbsolute(candidate) ||
    candidate.includes("\\") ||
    candidate.startsWith("/") ||
    posix.normalize(candidate) !== candidate ||
    candidate === "." ||
    candidate.startsWith("../") ||
    candidate.includes("/../")
  ) {
    throw new Error(`${description} must be a safe normalized relative path`);
  }
  return candidate;
}

function sha256(value, description) {
  const digest = string(value, description);
  if (!sha256Pattern.test(digest)) {
    throw new Error(`${description} must be a lower-case SHA-256`);
  }
  return digest;
}

function weight(value, description) {
  if (typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= 1000) {
    return;
  }
  if (typeof value === "string" && /^\d{1,4} \d{1,4}$/u.test(value)) {
    const [minimum, maximum] = value.split(" ").map(Number);
    if (
      minimum !== undefined &&
      maximum !== undefined &&
      minimum >= 1 &&
      maximum <= 1000 &&
      minimum < maximum
    ) {
      return;
    }
  }
  throw new Error(`${description} must be a CSS font weight or range`);
}

async function regularFile(root, relativePath) {
  const absoluteRoot = resolve(root);
  const absolutePath = resolve(absoluteRoot, ...relativePath.split("/"));
  const fromRoot = relative(absoluteRoot, absolutePath);
  if (fromRoot.startsWith(`..${sep}`) || fromRoot === ".." || isAbsolute(fromRoot)) {
    throw new Error(`${relativePath} escapes the font root`);
  }
  const status = await lstat(absolutePath);
  if (!status.isFile() || status.isSymbolicLink()) {
    throw new Error(`${relativePath} must be a regular file`);
  }
  return readFile(absolutePath);
}

function digest(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

async function inventory(root, current = "") {
  const entries = await readdir(resolve(root, ...current.split("/").filter(Boolean)), {
    withFileTypes: true
  });
  const paths = [];
  for (const entry of entries) {
    const entryPath = current.length === 0 ? entry.name : `${current}/${entry.name}`;
    if (entry.isSymbolicLink()) {
      throw new Error(`${entryPath} must not be a symbolic link`);
    }
    if (entry.isDirectory()) {
      paths.push(...(await inventory(root, entryPath)));
    } else if (entry.isFile()) {
      paths.push(entryPath);
    } else {
      throw new Error(`${entryPath} must be a regular file or directory`);
    }
  }
  return paths.sort();
}

export async function verifyFontDirectory(root) {
  const manifestBytes = await regularFile(root, "manifest.json");
  let decoded;
  try {
    decoded = JSON.parse(manifestBytes.toString("utf8"));
  } catch (error) {
    throw new Error("manifest.json must contain valid JSON", { cause: error });
  }
  const manifest = record(decoded, "font manifest");
  if (manifest.schemaVersion !== 1 || !Array.isArray(manifest.families)) {
    throw new Error("font manifest must use schemaVersion 1 and contain families");
  }
  if (manifest.families.length !== expectedFamilies.size) {
    throw new Error("font manifest must contain exactly three families");
  }

  const observedFamilies = new Set();
  const declaredPaths = new Set(["README.md", "manifest.json"]);
  for (const [familyIndex, familyValue] of manifest.families.entries()) {
    const family = record(familyValue, `families[${String(familyIndex)}]`);
    const familyName = string(family.family, `families[${String(familyIndex)}].family`);
    const expected = expectedFamilies.get(familyName);
    if (!expected || observedFamilies.has(familyName)) {
      throw new Error(`unexpected or duplicate font family ${JSON.stringify(familyName)}`);
    }
    observedFamilies.add(familyName);
    if (family.release !== expected.release) {
      throw new Error(`${familyName} must use release ${expected.release}`);
    }
    if (typeof family.modified !== "boolean" || family.modified) {
      throw new Error(`${familyName} must be recorded as unmodified`);
    }
    if (!commitPattern.test(string(family.commit, `${familyName} commit`))) {
      throw new Error(`${familyName} commit must be a 40-character Git object ID`);
    }
    for (const field of ["source", "download"]) {
      const url = new URL(string(family[field], `${familyName} ${field}`));
      if (url.protocol !== "https:" || url.hostname !== "github.com") {
        throw new Error(`${familyName} ${field} must be an official HTTPS GitHub URL`);
      }
    }

    const license = record(family.license, `${familyName} license`);
    const licensePath = safeRelativePath(license.path, `${familyName} license path`);
    if (licensePath !== expected.license || declaredPaths.has(licensePath)) {
      throw new Error(`${familyName} has an unexpected or duplicate license path`);
    }
    declaredPaths.add(licensePath);
    const licenseBytes = await regularFile(root, licensePath);
    if (digest(licenseBytes) !== sha256(license.sha256, `${familyName} license hash`)) {
      throw new Error(`${familyName} license hash does not match`);
    }
    const licenseText = licenseBytes.toString("utf8");
    if (
      licenseText.includes("MDREVIEW_PLACEHOLDER") ||
      !/SIL OPEN FONT LICENSE(?:\s+Version)?\s+1\.1/iu.test(licenseText)
    ) {
      throw new Error(`${familyName} license is missing the OFL 1.1 text`);
    }

    if (!Array.isArray(family.files) || family.files.length !== expected.files.size) {
      throw new Error(`${familyName} has the wrong number of font files`);
    }
    const observedFiles = new Set();
    for (const [fileIndex, fileValue] of family.files.entries()) {
      const font = record(fileValue, `${familyName} files[${String(fileIndex)}]`);
      const fontPath = safeRelativePath(font.path, `${familyName} font path`);
      if (
        !expected.files.has(fontPath) ||
        observedFiles.has(fontPath) ||
        declaredPaths.has(fontPath)
      ) {
        throw new Error(`${familyName} has an unexpected or duplicate font path`);
      }
      observedFiles.add(fontPath);
      declaredPaths.add(fontPath);
      const style = string(font.style, `${fontPath} style`);
      if (style !== "normal" && style !== "italic") {
        throw new Error(`${fontPath} style must be normal or italic`);
      }
      weight(font.weight, `${fontPath} weight`);
      const fontBytes = await regularFile(root, fontPath);
      const isWOFF2 = fontBytes.subarray(0, 4).toString("ascii") === "wOF2";
      const isTrueType =
        fontBytes[0] === 0 && fontBytes[1] === 1 && fontBytes[2] === 0 && fontBytes[3] === 0;
      if (fontBytes.length < 4 || (!isWOFF2 && !isTrueType)) {
        throw new Error(`${fontPath} is not a WOFF2 or TrueType font`);
      }
      if (digest(fontBytes) !== sha256(font.sha256, `${fontPath} hash`)) {
        throw new Error(`${fontPath} hash does not match`);
      }
    }
  }

  const actualPaths = await inventory(root);
  const expectedPaths = [...declaredPaths].sort();
  if (JSON.stringify(actualPaths) !== JSON.stringify(expectedPaths)) {
    throw new Error("font directory contains missing or unmanifested files");
  }
}

async function main() {
  const root = process.argv[2];
  if (!root) {
    throw new Error("usage: node scripts/verify-fonts.mjs <font-directory>");
  }
  await verifyFontDirectory(root);
  process.stdout.write(`verified bundled fonts in ${root}\n`);
}

const entryURL = process.argv[1] ? pathToFileURL(fileURLToPath(new URL(import.meta.url))).href : "";
if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === entryURL) {
  main().catch((error) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  });
}
