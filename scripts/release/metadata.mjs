#!/usr/bin/env node

import { createHash } from "node:crypto";
import { lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { basename, dirname, join, resolve, sep } from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const created = "2026-07-29T00:00:00Z";
const noticePattern = /^(?:licen[cs]e|copying|notice)(?:[._-].*)?$/iu;
const releaseEvidenceFiles = [
  "mdreview",
  "README.md",
  "SECURITY.md",
  "THIRD_PARTY_NOTICES.md",
  "LICENSE",
  "THIRD_PARTY_NOTICES.txt",
  "schema/review-v1.schema.json"
];

function fail(message) {
  throw new Error(message);
}

function sha256(data) {
  return createHash("sha256").update(data).digest("hex");
}

function stableID(prefix, identity) {
  return `SPDXRef-${prefix}-${sha256(identity).slice(0, 20)}`;
}

function asString(value, label) {
  if (typeof value !== "string" || value.length === 0) {
    fail(`${label} must be a non-empty string`);
  }
  return value;
}

function asObject(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    fail(`${label} must be an object`);
  }
  return value;
}

function readJSON(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    fail(`read ${label}: ${message}`);
  }
}

function run(command, arguments_, options = {}) {
  const result = spawnSync(command, arguments_, {
    cwd: options.cwd,
    encoding: "utf8",
    env: process.env,
    maxBuffer: 16 * 1024 * 1024
  });
  if (result.error !== undefined) {
    fail(`run ${command}: ${result.error.message}`);
  }
  if (result.status !== 0) {
    fail(
      `${command} ${arguments_.join(" ")} failed: ${result.stderr.trim() || result.stdout.trim()}`
    );
  }
  return result.stdout;
}

function parseTarget(value) {
  if (value !== "linux/amd64" && value !== "darwin/arm64") {
    fail(`unsupported release target: ${value}`);
  }
  const [goos, goarch] = value.split("/");
  return {
    value,
    goos,
    goarch,
    name: `${goos}-${goarch}`
  };
}

export function parseGoBuildInfo(output, targetValue = "linux/amd64") {
  const target = parseTarget(targetValue);
  const dependencies = [];
  const buildSettings = new Map();
  for (const line of output.split("\n")) {
    const fields = (line.startsWith("\t") ? line.slice(1) : line).split("\t");
    if (fields[0] === "dep") {
      if (fields.length < 4) {
        fail(`malformed Go dependency build-info line: ${line}`);
      }
      dependencies.push({
        path: asString(fields[1], "Go module path"),
        version: asString(fields[2], "Go module version"),
        sum: asString(fields[3], "Go module sum")
      });
    } else if (fields[0] === "build" && fields.length >= 2) {
      const separator = fields[1].indexOf("=");
      if (separator > 0) {
        buildSettings.set(fields[1].slice(0, separator), fields[1].slice(separator + 1));
      }
    }
  }
  if (dependencies.length === 0) {
    fail("completed binary contains no Go module dependencies");
  }
  const expectedSettings = [
    ["CGO_ENABLED", "0"],
    ["GOOS", target.goos],
    ["GOARCH", target.goarch]
  ];
  if (target.value === "linux/amd64") {
    expectedSettings.push(["GOAMD64", "v1"]);
  }
  for (const [key, expected] of expectedSettings) {
    if (buildSettings.get(key) !== expected) {
      fail(`completed binary build setting ${key} is not ${expected}`);
    }
  }
  dependencies.sort((left, right) =>
    `${left.path}@${left.version}`.localeCompare(`${right.path}@${right.version}`, "en")
  );
  return dependencies;
}

function goModuleSumChecksum(sum, label) {
  if (!sum.startsWith("h1:")) {
    fail(`${label} is not a Go h1 module sum`);
  }
  const bytes = Buffer.from(sum.slice(3), "base64");
  if (bytes.length !== 32) {
    fail(`${label} does not contain a SHA-256 value`);
  }
  return bytes.toString("hex");
}

function npmIntegrityChecksum(integrity, label) {
  const match = /^sha512-([A-Za-z0-9+/]+={0,2})$/u.exec(integrity);
  if (match === null) {
    fail(`${label} does not use one exact SHA-512 integrity value`);
  }
  const bytes = Buffer.from(match[1], "base64");
  if (bytes.length !== 64) {
    fail(`${label} does not contain a SHA-512 value`);
  }
  return bytes.toString("hex");
}

function npmPathName(packagePath) {
  const marker = "node_modules/";
  const index = packagePath.lastIndexOf(marker);
  if (index < 0) {
    fail(`npm lock path is not a package path: ${packagePath}`);
  }
  const remainder = packagePath.slice(index + marker.length);
  const segments = remainder.split("/");
  return segments[0].startsWith("@") ? `${segments[0]}/${segments[1]}` : segments[0];
}

function npmPURL(name, packageVersion) {
  if (name.startsWith("@")) {
    const [scope, packageName] = name.split("/");
    return `pkg:npm/${encodeURIComponent(scope)}/${encodeURIComponent(packageName)}@${encodeURIComponent(packageVersion)}`;
  }
  return `pkg:npm/${encodeURIComponent(name)}@${encodeURIComponent(packageVersion)}`;
}

function noticeFiles(packageDirectory, label) {
  const files = readdirSync(packageDirectory, { withFileTypes: true })
    .filter((entry) => entry.isFile() && noticePattern.test(entry.name))
    .map((entry) => entry.name)
    .sort();
  if (files.length === 0) {
    fail(`${label} has no attributable licence or notice file`);
  }
  return files.map((name) => ({
    name,
    text: readFileSync(join(packageDirectory, name), "utf8")
  }));
}

export function collectNpmPackages(sourceRoot) {
  const lock = asObject(
    readJSON(join(sourceRoot, "web/package-lock.json"), "npm lock file"),
    "npm lock file"
  );
  if (lock.lockfileVersion !== 3) {
    fail("npm lock file must use lockfileVersion 3");
  }
  const packages = asObject(lock.packages, "npm lock packages");
  const result = [];

  for (const packagePath of Object.keys(packages).filter(Boolean).sort()) {
    const entry = asObject(packages[packagePath], `npm package ${packagePath}`);
    const packageVersion = asString(entry.version, `${packagePath} version`);
    const declaredLicense = asString(entry.license, `${packagePath} licence`);
    const integrity = asString(entry.integrity, `${packagePath} integrity`);
    const resolvedLocation = asString(entry.resolved, `${packagePath} resolved location`);
    if (!resolvedLocation.startsWith("https://registry.npmjs.org/")) {
      fail(`${packagePath} does not resolve from the pinned npm registry`);
    }

    const isBuildDependency = entry.dev === true;
    const packageDirectory = join(sourceRoot, "web", ...packagePath.split("/"));
    let installedName = npmPathName(packagePath);
    if (!isBuildDependency) {
      const info = lstatSync(packageDirectory);
      if (!info.isDirectory()) {
        fail(`${packagePath} is not an installed package directory`);
      }
      const installedManifest = asObject(
        readJSON(join(packageDirectory, "package.json"), `${packagePath} installed manifest`),
        `${packagePath} installed manifest`
      );
      installedName = asString(installedManifest.name, `${packagePath} installed name`);
      const installedVersion = asString(
        installedManifest.version,
        `${packagePath} installed version`
      );
      if (installedVersion !== packageVersion) {
        fail(`${packagePath} installed version differs from its lock entry`);
      }
    }
    result.push({
      packagePath,
      name: installedName,
      version: packageVersion,
      declaredLicense,
      integrity,
      resolvedLocation,
      isBuildDependency,
      notices: isBuildDependency
        ? []
        : noticeFiles(packageDirectory, `${installedName}@${packageVersion}`)
    });
  }
  if (result.length === 0) {
    fail("npm lock file contains no package entries");
  }
  return result;
}

function collectGoModules(sourceRoot, binaryPath, target) {
  const buildInfo = run("go", ["version", "-m", binaryPath], { cwd: sourceRoot });
  const dependencies = parseGoBuildInfo(buildInfo, target.value);
  const policy = asObject(
    readJSON(join(sourceRoot, "scripts/release/go-licenses.json"), "Go licence policy"),
    "Go licence policy"
  );
  if (policy.schemaVersion !== 1 || !Array.isArray(policy.modules)) {
    fail("Go licence policy must use schemaVersion 1 and contain modules");
  }
  const policies = new Map();
  for (const raw of policy.modules) {
    const module = asObject(raw, "Go licence module");
    const key = `${asString(module.path, "Go licence path")}@${asString(module.version, "Go licence version")}`;
    if (policies.has(key)) {
      fail(`duplicate Go licence policy: ${key}`);
    }
    policies.set(key, module);
  }

  const result = [];
  for (const dependency of dependencies) {
    const key = `${dependency.path}@${dependency.version}`;
    const modulePolicy = policies.get(key);
    if (modulePolicy === undefined) {
      fail(`linked Go module has no frozen licence evidence: ${key}`);
    }
    const download = asObject(
      JSON.parse(run("go", ["mod", "download", "-json", key], { cwd: sourceRoot })),
      `downloaded Go module ${key}`
    );
    if (download.Sum !== dependency.sum) {
      fail(`downloaded Go module sum differs from the linked binary: ${key}`);
    }
    const moduleDirectory = resolve(asString(download.Dir, `${key} module directory`));
    const rawFiles = modulePolicy.files;
    if (!Array.isArray(rawFiles) || rawFiles.length === 0) {
      fail(`Go licence policy has no files: ${key}`);
    }
    const notices = rawFiles.map((rawFile) => {
      const file = asObject(rawFile, `${key} licence file`);
      const relativePath = asString(file.path, `${key} licence path`);
      if (relativePath !== basename(relativePath)) {
        fail(`Go licence path must be a module-root file: ${key} ${relativePath}`);
      }
      const data = readFileSync(join(moduleDirectory, relativePath));
      const expectedHash = asString(file.sha256, `${key} licence SHA-256`);
      if (sha256(data) !== expectedHash) {
        fail(`Go licence evidence hash differs: ${key} ${relativePath}`);
      }
      return { name: relativePath, text: data.toString("utf8") };
    });
    result.push({
      ...dependency,
      declaredLicense: asString(modulePolicy.license, `${key} licence`),
      notices
    });
    policies.delete(key);
  }
  if (policies.size !== 0) {
    fail(
      `Go licence policy contains modules absent from the binary: ${[...policies.keys()].join(", ")}`
    );
  }
  return { buildInfo, modules: result };
}

function collectFonts(sourceRoot) {
  const fontRoot = join(sourceRoot, "web/dist/fonts");
  const manifest = asObject(
    readJSON(join(fontRoot, "manifest.json"), "font manifest"),
    "font manifest"
  );
  if (manifest.schemaVersion !== 1 || !Array.isArray(manifest.families)) {
    fail("font manifest must use schemaVersion 1 and contain families");
  }
  return manifest.families.map((rawFamily) => {
    const family = asObject(rawFamily, "font family");
    const familyName = asString(family.family, "font family name");
    const release = asString(family.release, `${familyName} release`);
    const license = asObject(family.license, `${familyName} licence`);
    const licensePath = asString(license.path, `${familyName} licence path`);
    const licenseData = readFileSync(join(fontRoot, ...licensePath.split("/")));
    if (sha256(licenseData) !== asString(license.sha256, `${familyName} licence SHA-256`)) {
      fail(`${familyName} licence hash differs from the font manifest`);
    }
    if (!Array.isArray(family.files) || family.files.length === 0) {
      fail(`${familyName} has no font files`);
    }
    const files = family.files.map((rawFile) => {
      const file = asObject(rawFile, `${familyName} font file`);
      const path = asString(file.path, `${familyName} font path`);
      const data = readFileSync(join(fontRoot, ...path.split("/")));
      const expectedHash = asString(file.sha256, `${familyName} font SHA-256`);
      if (sha256(data) !== expectedHash) {
        fail(`${familyName} font hash differs: ${path}`);
      }
      return { path, sha256: expectedHash };
    });
    return {
      name: familyName,
      release,
      source: asString(family.source, `${familyName} source`),
      licensePath,
      licenseText: licenseData.toString("utf8"),
      files
    };
  });
}

function noticeSection(heading, declaredLicense, files) {
  const parts = [`## ${heading}\n\nSPDX licence: ${declaredLicense}\n`];
  for (const file of files) {
    parts.push(`\n### ${file.name}\n\n${file.text.trimEnd()}\n`);
  }
  return parts.join("");
}

function writeNotices(outputPath, version, goModules, npmPackages, fonts) {
  const parts = [
    "# mdReview third-party notices\n\n",
    "This deterministic notice file records licence and notice text for code and fonts redistributed ",
    `in the mdReview ${version} binary. Build-only frontend packages remain recorded in the SPDX SBOM `,
    "but are not represented here as redistributed runtime content.\n"
  ];
  for (const module of goModules) {
    parts.push(
      "\n",
      noticeSection(
        `Go module ${module.path}@${module.version}`,
        module.declaredLicense,
        module.notices
      )
    );
  }
  for (const package_ of npmPackages.filter((entry) => !entry.isBuildDependency)) {
    parts.push(
      "\n",
      noticeSection(
        `npm package ${package_.name}@${package_.version}`,
        package_.declaredLicense,
        package_.notices
      )
    );
  }
  for (const font of fonts) {
    parts.push(
      "\n",
      noticeSection(`Font ${font.name} ${font.release}`, "OFL-1.1", [
        { name: font.licensePath, text: font.licenseText }
      ])
    );
  }
  writeFileSync(outputPath, `${parts.join("").trimEnd()}\n`, { mode: 0o644 });
}

function fileSPDXID(path) {
  return stableID("File", path);
}

function fileEntry(stagingRoot, path, licenseConcluded = "NOASSERTION") {
  const data = readFileSync(join(stagingRoot, ...path.split("/")));
  return {
    fileName: `./${path}`,
    SPDXID: fileSPDXID(path),
    checksums: [{ algorithm: "SHA256", checksumValue: sha256(data) }],
    licenseConcluded,
    copyrightText: "NOASSERTION"
  };
}

function npmPackageEntry(package_) {
  const id = stableID("Npm", package_.packagePath);
  return {
    name: package_.name,
    SPDXID: id,
    versionInfo: package_.version,
    downloadLocation: package_.resolvedLocation,
    filesAnalyzed: false,
    checksums: [
      {
        algorithm: "SHA512",
        checksumValue: npmIntegrityChecksum(package_.integrity, `${package_.packagePath} integrity`)
      }
    ],
    licenseConcluded: package_.isBuildDependency ? "NOASSERTION" : package_.declaredLicense,
    licenseDeclared: package_.declaredLicense,
    copyrightText: "NOASSERTION",
    primaryPackagePurpose: package_.isBuildDependency ? "BUILD_TOOL" : "LIBRARY",
    externalRefs: [
      {
        referenceCategory: "PACKAGE-MANAGER",
        referenceType: "purl",
        referenceLocator: npmPURL(package_.name, package_.version)
      }
    ]
  };
}

function goPackageEntry(module) {
  return {
    name: module.path,
    SPDXID: stableID("Go", `${module.path}@${module.version}`),
    versionInfo: module.version,
    downloadLocation: "NOASSERTION",
    filesAnalyzed: false,
    checksums: [
      {
        algorithm: "SHA256",
        checksumValue: goModuleSumChecksum(module.sum, `${module.path}@${module.version} sum`)
      }
    ],
    licenseConcluded: module.declaredLicense,
    licenseDeclared: module.declaredLicense,
    copyrightText: "NOASSERTION",
    primaryPackagePurpose: "LIBRARY",
    externalRefs: [
      {
        referenceCategory: "PACKAGE-MANAGER",
        referenceType: "purl",
        referenceLocator: `pkg:golang/${module.path
          .split("/")
          .map((segment) => encodeURIComponent(segment))
          .join("/")}@${encodeURIComponent(module.version)}`
      }
    ]
  };
}

function fontPackageEntry(font) {
  return {
    name: font.name,
    SPDXID: stableID("Font", `${font.name}@${font.release}`),
    versionInfo: font.release,
    downloadLocation: font.source,
    filesAnalyzed: false,
    licenseConcluded: "OFL-1.1",
    licenseDeclared: "OFL-1.1",
    copyrightText: "NOASSERTION",
    primaryPackagePurpose: "OTHER"
  };
}

function writeSPDX(
  outputPath,
  stagingRoot,
  version,
  target,
  binaryHash,
  goModules,
  npmPackages,
  fonts
) {
  const applicationID = "SPDXRef-Package-mdReview";
  const files = releaseEvidenceFiles.map((path) =>
    fileEntry(stagingRoot, path, path === "LICENSE" ? "MIT" : "NOASSERTION")
  );
  for (const font of fonts) {
    for (const file of font.files) {
      const path = `embedded-fonts/${file.path}`;
      files.push({
        fileName: `./${path}`,
        SPDXID: fileSPDXID(path),
        checksums: [{ algorithm: "SHA256", checksumValue: file.sha256 }],
        licenseConcluded: "OFL-1.1",
        copyrightText: "NOASSERTION"
      });
    }
  }

  const packages = [
    {
      name: "mdReview",
      SPDXID: applicationID,
      versionInfo: version,
      downloadLocation: "NOASSERTION",
      filesAnalyzed: false,
      checksums: [{ algorithm: "SHA256", checksumValue: binaryHash }],
      licenseConcluded: "MIT",
      licenseDeclared: "MIT",
      copyrightText: "Copyright (c) 2026 mdReview contributors",
      primaryPackagePurpose: "APPLICATION"
    },
    ...goModules.map(goPackageEntry),
    ...npmPackages.map(npmPackageEntry),
    ...fonts.map(fontPackageEntry)
  ];

  const relationships = [
    {
      spdxElementId: "SPDXRef-DOCUMENT",
      relationshipType: "DESCRIBES",
      relatedSpdxElement: applicationID
    }
  ];
  for (const path of releaseEvidenceFiles) {
    relationships.push({
      spdxElementId: applicationID,
      relationshipType: "CONTAINS",
      relatedSpdxElement: fileSPDXID(path)
    });
  }
  for (const module of goModules) {
    relationships.push({
      spdxElementId: applicationID,
      relationshipType: "DEPENDS_ON",
      relatedSpdxElement: stableID("Go", `${module.path}@${module.version}`)
    });
  }
  for (const package_ of npmPackages) {
    relationships.push({
      spdxElementId: package_.isBuildDependency
        ? stableID("Npm", package_.packagePath)
        : applicationID,
      relationshipType: package_.isBuildDependency ? "BUILD_DEPENDENCY_OF" : "DEPENDS_ON",
      relatedSpdxElement: package_.isBuildDependency
        ? applicationID
        : stableID("Npm", package_.packagePath)
    });
  }
  for (const font of fonts) {
    const fontID = stableID("Font", `${font.name}@${font.release}`);
    relationships.push({
      spdxElementId: applicationID,
      relationshipType: "CONTAINS",
      relatedSpdxElement: fontID
    });
    for (const file of font.files) {
      relationships.push({
        spdxElementId: fontID,
        relationshipType: "CONTAINS",
        relatedSpdxElement: fileSPDXID(`embedded-fonts/${file.path}`)
      });
    }
  }

  const document = {
    spdxVersion: "SPDX-2.3",
    dataLicense: "CC0-1.0",
    SPDXID: "SPDXRef-DOCUMENT",
    name: `mdreview-${version}-${target.name}`,
    documentNamespace: `https://mdreview.dev/spdx/${version}/${target.name}/${binaryHash}`,
    creationInfo: {
      created,
      creators: ["Tool: mdReview deterministic release metadata generator"]
    },
    packages,
    files,
    relationships
  };
  writeFileSync(outputPath, `${JSON.stringify(document, null, 2)}\n`, { mode: 0o644 });
}

export function generateMetadata(
  sourceRootPath,
  binaryPathValue,
  outputDirectoryPath,
  targetValue
) {
  const sourceRoot = resolve(sourceRootPath);
  const binaryPath = resolve(binaryPathValue);
  const outputDirectory = resolve(outputDirectoryPath);
  const target = parseTarget(targetValue);
  const version = readFileSync(join(sourceRoot, "internal/version/version.txt"), "utf8").trim();
  if (!/^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/u.test(version)) {
    fail("product version is invalid");
  }
  mkdirSync(outputDirectory, { recursive: true });

  const binaryData = readFileSync(binaryPath);
  const binaryHash = sha256(binaryData);
  const { buildInfo, modules: goModules } = collectGoModules(sourceRoot, binaryPath, target);
  const npmPackages = collectNpmPackages(sourceRoot);
  const fonts = collectFonts(sourceRoot);
  const noticePath = join(outputDirectory, "THIRD_PARTY_NOTICES.txt");
  const spdxPath = join(outputDirectory, "mdreview.spdx.json");

  writeNotices(noticePath, version, goModules, npmPackages, fonts);
  writeSPDX(spdxPath, outputDirectory, version, target, binaryHash, goModules, npmPackages, fonts);
  return { binaryHash, buildInfo, noticePath, spdxPath };
}

function main() {
  const [, , sourceRoot, binaryPath, outputDirectory, target] = process.argv;
  if (target === undefined) {
    fail("usage: metadata.mjs SOURCE_ROOT BINARY OUTPUT_DIRECTORY TARGET");
  }
  const result = generateMetadata(sourceRoot, binaryPath, outputDirectory, target);
  process.stdout.write(result.buildInfo);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`generate release metadata: ${message}\n`);
    process.exitCode = 1;
  }
}
