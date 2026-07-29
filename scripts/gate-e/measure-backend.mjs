import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rename,
  rm,
  stat,
  writeFile
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join, relative, resolve, sep } from "node:path";
import process from "node:process";
import { createRequire } from "node:module";

const generatorVersion = "gate-e-backend-v1";
const reportedSampleCount = 5;
const staleAgeNanoseconds = 1_100_000_000n;
const idleWindowNanoseconds = 1_000_000_000n;
const ignoredDocumentCount = 5_000;
const maximumAssetBytes = 20 * 1024 * 1024;

function fail(message) {
  throw new Error(message);
}

function parseArguments() {
  const [projectDirectoryArgument, outputDirectoryArgument, binaryPathArgument] =
    process.argv.slice(2);
  if (!projectDirectoryArgument || !outputDirectoryArgument || !binaryPathArgument) {
    fail("usage: measure-backend.mjs <project-directory> <output-directory> <binary>");
  }
  return {
    binaryPath: resolve(binaryPathArgument),
    outputDirectory: resolve(outputDirectoryArgument),
    projectDirectory: resolve(projectDirectoryArgument)
  };
}

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function stableJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function elapsedMilliseconds(startNanoseconds, endNanoseconds = process.hrtime.bigint()) {
  return Number(endNanoseconds - startNanoseconds) / 1_000_000;
}

function round(value, digits = 3) {
  return Number(value.toFixed(digits));
}

function median(values) {
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.floor(sorted.length / 2)];
}

function commandOutput(command, commandArguments = []) {
  const result = spawnSync(command, commandArguments, {
    encoding: "utf8",
    env: process.env
  });
  if (result.status !== 0) {
    return `unavailable (${command} exited ${String(result.status)}: ${result.stderr.trim()})`;
  }
  return result.stdout.trim();
}

async function pathManifest(rootDirectory, includedPaths) {
  const files = [];

  async function visit(absolutePath) {
    const pathStat = await stat(absolutePath);
    if (pathStat.isDirectory()) {
      const entries = await readdir(absolutePath);
      entries.sort();
      for (const entry of entries) {
        await visit(join(absolutePath, entry));
      }
      return;
    }
    if (!pathStat.isFile()) {
      return;
    }
    const bytes = await readFile(absolutePath);
    files.push({
      path: relative(rootDirectory, absolutePath).split(sep).join("/"),
      sha256: sha256(bytes),
      sizeBytes: bytes.length
    });
  }

  for (const includedPath of includedPaths) {
    await visit(join(rootDirectory, includedPath));
  }
  files.sort((left, right) => left.path.localeCompare(right.path, "en"));
  return files;
}

async function writeManifest(outputPath, manifest) {
  const bytes = stableJSON(manifest);
  await writeFile(outputPath, bytes, "utf8");
  return {
    path: basename(outputPath),
    sha256: sha256(bytes)
  };
}

function documentName(index, count) {
  const width = Math.max(2, String(count - 1).length);
  return `document-${String(index).padStart(width, "0")}.md`;
}

function documentSource(fixtureName, index) {
  return [
    `# ${fixtureName} document ${String(index)}`,
    "",
    "Deterministic Gate E fixture content.",
    `Sequence: ${String(index).padStart(4, "0")}`,
    ""
  ].join("\n");
}

function emptySidecar() {
  return '{\n  "schemaVersion": 1,\n  "threads": []\n}\n';
}

function sidecarWithMessage(messageBody, sequence) {
  return `${JSON.stringify(
    {
      schemaVersion: 1,
      threads: [
        {
          id: `thread_gate_e_${String(sequence)}`,
          anchor: {
            type: "document"
          },
          status: "open",
          messages: [
            {
              id: `message_gate_e_${String(sequence)}`,
              author: {
                type: "agent",
                name: "Gate E fixture"
              },
              body: messageBody,
              createdAt: "2026-07-29T12:00:00Z"
            }
          ]
        }
      ]
    },
    null,
    2
  )}\n`;
}

async function writeVisibleDocuments(directory, fixtureName, count) {
  await mkdir(directory, { recursive: true });
  for (let index = 0; index < count; index += 1) {
    const name = documentName(index, count);
    await writeFile(join(directory, name), documentSource(fixtureName, index), "utf8");
    await writeFile(join(directory, `${name}.review.json`), emptySidecar(), "utf8");
  }
}

async function generateFixture(outputDirectory, name, visibleDocumentCount, ignoredCount = 0) {
  const fixtureDirectory = join(outputDirectory, "fixtures", name);
  await writeVisibleDocuments(fixtureDirectory, name, visibleDocumentCount);
  if (ignoredCount > 0) {
    await writeFile(join(fixtureDirectory, ".gitignore"), "ignored/\n", "utf8");
    const ignoredDirectory = join(fixtureDirectory, "ignored");
    await mkdir(ignoredDirectory);
    for (let index = 0; index < ignoredCount; index += 1) {
      const name = documentName(index, ignoredCount);
      await writeFile(
        join(ignoredDirectory, name),
        documentSource(`${name}-ignored`, index),
        "utf8"
      );
    }
  }

  const files = await pathManifest(fixtureDirectory, ["."]);
  const manifest = {
    generatorVersion,
    ignoredDocumentCount: ignoredCount,
    name,
    visibleDocumentCount,
    files
  };
  const manifestPath = join(outputDirectory, "manifests", `${name}.json`);
  const manifestRecord = await writeManifest(manifestPath, manifest);
  return {
    directory: fixtureDirectory,
    fileCount: files.length,
    ignoredDocumentCount: ignoredCount,
    manifest: {
      path: `manifests/${manifestRecord.path}`,
      sha256: manifestRecord.sha256
    },
    name,
    visibleDocumentCount
  };
}

async function generatedFixtureRecord(
  outputDirectory,
  name,
  fixtureDirectory,
  visibleDocumentCount
) {
  const files = await pathManifest(fixtureDirectory, ["."]);
  const manifestRecord = await writeManifest(join(outputDirectory, "manifests", `${name}.json`), {
    files,
    generatorVersion,
    ignoredDocumentCount: 0,
    name,
    visibleDocumentCount
  });
  return {
    directory: fixtureDirectory,
    fileCount: files.length,
    ignoredDocumentCount: 0,
    manifest: {
      path: `manifests/${manifestRecord.path}`,
      sha256: manifestRecord.sha256
    },
    name,
    visibleDocumentCount
  };
}

async function generateImageHeavyFixture(outputDirectory) {
  const name = "image-heavy";
  const fixtureDirectory = join(outputDirectory, "fixtures", name);
  await mkdir(fixtureDirectory, { recursive: true });
  const source = [
    "# Image-heavy Gate E fixture",
    "",
    "![Allowed image](allowed.png)",
    "![Repeated allowed image](allowed.png)",
    "![Missing image](missing.png)",
    "![Unsupported active SVG](active.svg)",
    "![Oversized image](oversized.png)",
    ""
  ].join("\n");
  await writeFile(join(fixtureDirectory, "images.md"), source, "utf8");
  await writeFile(join(fixtureDirectory, "images.md.review.json"), emptySidecar(), "utf8");
  await writeFile(
    join(fixtureDirectory, "allowed.png"),
    Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
      "base64"
    )
  );
  await writeFile(
    join(fixtureDirectory, "active.svg"),
    '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>\n',
    "utf8"
  );
  const oversizedPNG = Buffer.alloc(maximumAssetBytes + 1);
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]).copy(oversizedPNG);
  await writeFile(join(fixtureDirectory, "oversized.png"), oversizedPNG);
  return generatedFixtureRecord(outputDirectory, name, fixtureDirectory, 1);
}

async function generateExternalChangeFixture(outputDirectory) {
  const name = "external-change";
  const fixtureDirectory = join(outputDirectory, "fixtures", name);
  await mkdir(fixtureDirectory, { recursive: true });
  await writeFile(join(fixtureDirectory, "external.md"), documentSource(name, 0), "utf8");
  await writeFile(join(fixtureDirectory, "external.md.review.json"), emptySidecar(), "utf8");
  return generatedFixtureRecord(outputDirectory, name, fixtureDirectory, 1);
}

async function generateFixtures(outputDirectory) {
  await mkdir(join(outputDirectory, "fixtures"), { recursive: true });
  await mkdir(join(outputDirectory, "manifests"), { recursive: true });
  const fixtures = [
    await generateFixture(outputDirectory, "workspace-10", 10),
    await generateFixture(outputDirectory, "workspace-100", 100),
    await generateFixture(outputDirectory, "workspace-1000", 1_000),
    await generateFixture(outputDirectory, "ignored-tree", 100, ignoredDocumentCount),
    await generateImageHeavyFixture(outputDirectory),
    await generateExternalChangeFixture(outputDirectory)
  ];
  const aggregateManifest = {
    fixtures: fixtures.map((fixture) => ({
      fileCount: fixture.fileCount,
      ignoredDocumentCount: fixture.ignoredDocumentCount,
      manifestSha256: fixture.manifest.sha256,
      name: fixture.name,
      visibleDocumentCount: fixture.visibleDocumentCount
    })),
    generatorVersion
  };
  const aggregateRecord = await writeManifest(
    join(outputDirectory, "manifests", "fixtures.json"),
    aggregateManifest
  );
  return {
    aggregateManifest: {
      path: `manifests/${aggregateRecord.path}`,
      sha256: aggregateRecord.sha256
    },
    fixtures
  };
}

function executableVersion(executablePath, versionArgument) {
  if (!executablePath) {
    return "unavailable (executable path missing)";
  }
  return commandOutput(executablePath, [versionArgument]);
}

async function browserVersions(projectDirectory) {
  try {
    const require = createRequire(import.meta.url);
    const playwright = require(
      join(projectDirectory, "web", "node_modules", "@playwright", "test")
    );
    return {
      chromium: executableVersion(playwright.chromium.executablePath(), "--version"),
      firefox: executableVersion(playwright.firefox.executablePath(), "--version"),
      playwright: commandOutput("npm", [
        "--prefix",
        join(projectDirectory, "web"),
        "exec",
        "playwright",
        "--",
        "--version"
      ])
    };
  } catch (error) {
    return {
      chromium: `unavailable (${String(error)})`,
      firefox: `unavailable (${String(error)})`,
      playwright: `unavailable (${String(error)})`
    };
  }
}

async function environmentRecord(projectDirectory, outputDirectory) {
  let distribution = "unavailable";
  try {
    const osRelease = await readFile("/etc/os-release", "utf8");
    const fields = Object.fromEntries(
      osRelease
        .split("\n")
        .filter((line) => line.includes("="))
        .map((line) => {
          const separator = line.indexOf("=");
          return [line.slice(0, separator), line.slice(separator + 1).replace(/^"|"$/gu, "")];
        })
    );
    distribution = fields.PRETTY_NAME ?? `${fields.NAME ?? "Linux"} ${fields.VERSION ?? ""}`;
  } catch {
    distribution = "unavailable (/etc/os-release could not be read)";
  }

  const cpuInfo = await readFile("/proc/cpuinfo", "utf8");
  const cpuModel =
    cpuInfo.match(/^model name\s*:\s*(.+)$/mu)?.[1] ??
    cpuInfo.match(/^Hardware\s*:\s*(.+)$/mu)?.[1] ??
    "unavailable";
  const memoryInfo = await readFile("/proc/meminfo", "utf8");
  const installedMemoryKiB = Number(memoryInfo.match(/^MemTotal:\s+(\d+)\s+kB$/mu)?.[1]);
  const filesystemType = commandOutput("stat", ["-f", "-c", "%T", outputDirectory]);
  const browsers = await browserVersions(projectDirectory);

  return {
    architecture: commandOutput("uname", ["-m"]),
    browsers,
    cpu: {
      logicalCount: Number(commandOutput("getconf", ["_NPROCESSORS_ONLN"])),
      model: cpuModel
    },
    distribution,
    filesystem: {
      classification: ["ext2/ext3", "ext2/ext3", "xfs", "btrfs"].includes(filesystemType)
        ? "local"
        : "virtualised, container, network, or other",
      type: filesystemType
    },
    go: commandOutput("go", ["version"]),
    installedMemoryKiB,
    kernel: commandOutput("uname", ["-sr"]),
    node: process.version,
    npm: commandOutput("npm", ["--version"])
  };
}

async function artifactChecksums(projectDirectory, outputDirectory, binaryPath) {
  const sourceFiles = await pathManifest(projectDirectory, [
    "cmd",
    "internal",
    "go.mod",
    "go.sum",
    "web/embed.go",
    "scripts/gate-e"
  ]);
  const frontendFiles = await pathManifest(projectDirectory, ["web/dist"]);
  const scriptFiles = await pathManifest(projectDirectory, ["scripts/gate-e"]);
  const sourceRecord = await writeManifest(join(outputDirectory, "manifests", "source.json"), {
    files: sourceFiles
  });
  const frontendRecord = await writeManifest(
    join(outputDirectory, "manifests", "frontend-assets.json"),
    { files: frontendFiles }
  );
  const scriptRecord = await writeManifest(
    join(outputDirectory, "manifests", "measurement-scripts.json"),
    { files: scriptFiles }
  );
  const binaryBytes = await readFile(binaryPath);
  return {
    binary: {
      path: "mdreview",
      sha256: sha256(binaryBytes),
      sizeBytes: binaryBytes.length
    },
    frontendAssetManifest: {
      path: `manifests/${frontendRecord.path}`,
      sha256: frontendRecord.sha256
    },
    measurementScriptManifest: {
      path: `manifests/${scriptRecord.path}`,
      sha256: scriptRecord.sha256
    },
    sourceManifest: {
      path: `manifests/${sourceRecord.path}`,
      sha256: sourceRecord.sha256
    }
  };
}

async function readProcessSample(pid) {
  const [statusText, statText] = await Promise.all([
    readFile(`/proc/${String(pid)}/status`, "utf8"),
    readFile(`/proc/${String(pid)}/stat`, "utf8")
  ]);
  const rssKiB = Number(statusText.match(/^VmRSS:\s+(\d+)\s+kB$/mu)?.[1]);
  const closingParenthesis = statText.lastIndexOf(")");
  if (closingParenthesis < 0) {
    fail(`cannot parse /proc/${String(pid)}/stat`);
  }
  const fieldsAfterCommand = statText
    .slice(closingParenthesis + 2)
    .trim()
    .split(/\s+/u);
  const userTicks = Number(fieldsAfterCommand[11]);
  const systemTicks = Number(fieldsAfterCommand[12]);
  if (![rssKiB, userTicks, systemTicks].every(Number.isFinite)) {
    fail(`cannot parse process RSS or CPU ticks for pid ${String(pid)}`);
  }
  return {
    cpuTicks: userTicks + systemTicks,
    rssKiB
  };
}

async function waitUntil(targetNanoseconds) {
  for (;;) {
    const remaining = targetNanoseconds - process.hrtime.bigint();
    if (remaining <= 0n) {
      return;
    }
    const remainingMilliseconds = Number(remaining / 1_000_000n);
    await new Promise((resolvePromise) =>
      setTimeout(resolvePromise, Math.max(1, remainingMilliseconds))
    );
  }
}

async function stopServer(server) {
  if (server.child.exitCode !== null || server.child.signalCode !== null) {
    return;
  }
  const exited = new Promise((resolvePromise) => {
    server.child.once("exit", resolvePromise);
  });
  server.child.kill("SIGINT");
  const graceful = await Promise.race([
    exited.then(() => true),
    new Promise((resolvePromise) => setTimeout(() => resolvePromise(false), 5_000))
  ]);
  if (!graceful && server.child.exitCode === null && server.child.signalCode === null) {
    server.child.kill("SIGKILL");
    await exited;
  }
}

async function launchServer(binaryPath, fixture, runtimeRoot, runName) {
  const runtimeDirectory = join(runtimeRoot, runName);
  await mkdir(runtimeDirectory, { recursive: true, mode: 0o700 });
  await chmod(runtimeDirectory, 0o700);
  const startedNanoseconds = process.hrtime.bigint();
  const child = spawn(binaryPath, [fixture.directory], {
    env: {
      ...process.env,
      MDREVIEW_GATE_E_COUNTERS: "1",
      XDG_RUNTIME_DIR: runtimeDirectory
    },
    stdio: ["ignore", "pipe", "pipe"]
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");

  let output = "";
  let errorOutput = "";
  child.stdout.on("data", (chunk) => {
    output += chunk;
  });
  child.stderr.on("data", (chunk) => {
    errorOutput += chunk;
  });

  const readiness = new Promise((resolvePromise, rejectPromise) => {
    const handleExit = (code, signal) => {
      rejectPromise(
        new Error(
          `mdReview exited before readiness (code ${String(code)}, signal ${String(signal)}): ${errorOutput}`
        )
      );
    };
    child.once("exit", handleExit);

    const inspectOutput = () => {
      if (!output.includes("Press Ctrl+C to stop.")) {
        return;
      }
      child.off("exit", handleExit);
      const instanceURL = output.match(/^URL:\s+(\S+)$/mu)?.[1];
      const documentCount = Number(output.match(/^Documents:\s+(\d+)$/mu)?.[1]);
      if (!instanceURL || !Number.isInteger(documentCount)) {
        rejectPromise(new Error(`mdReview printed incomplete readiness output:\n${output}`));
        return;
      }
      const parsedURL = new URL(instanceURL);
      if (parsedURL.search || parsedURL.hash) {
        rejectPromise(new Error("mdReview readiness URL must not contain a query or fragment"));
        return;
      }
      resolvePromise({
        baseURL: parsedURL.toString().replace(/\/$/u, ""),
        documentCount,
        readyNanoseconds: process.hrtime.bigint()
      });
    };
    child.stdout.on("data", inspectOutput);
  });

  let timeoutID;
  const timeout = new Promise((_, rejectPromise) => {
    timeoutID = setTimeout(
      () => rejectPromise(new Error(`mdReview readiness timed out:\n${output}`)),
      10_000
    );
  });

  try {
    const ready = await Promise.race([readiness, timeout]);
    clearTimeout(timeoutID);
    if (ready.documentCount !== fixture.visibleDocumentCount) {
      fail(
        `${fixture.name} startup reported ${String(ready.documentCount)} documents, expected ${String(fixture.visibleDocumentCount)}`
      );
    }
    return {
      ...ready,
      child,
      processReadyMs: round(elapsedMilliseconds(startedNanoseconds, ready.readyNanoseconds))
    };
  } catch (error) {
    clearTimeout(timeoutID);
    await stopServer({ child });
    throw error;
  }
}

async function stateRequest(server, since) {
  const query = since === undefined ? "" : `?since=${String(since)}`;
  const startedNanoseconds = process.hrtime.bigint();
  const response = await fetch(`${server.baseURL}/api/state${query}`);
  const body = await response.json();
  const completedNanoseconds = process.hrtime.bigint();
  if (response.status !== 200) {
    fail(`state request failed with HTTP ${String(response.status)}: ${JSON.stringify(body)}`);
  }
  if (
    typeof body !== "object" ||
    body === null ||
    !Number.isInteger(body.workspaceRevision) ||
    !["changed", "unchanged"].includes(body.status)
  ) {
    fail(`state request returned an invalid payload: ${JSON.stringify(body)}`);
  }
  return {
    body,
    completedNanoseconds,
    elapsedMs: round(elapsedMilliseconds(startedNanoseconds, completedNanoseconds))
  };
}

async function jsonRequest(server, path) {
  const response = await fetch(`${server.baseURL}${path}`);
  const body = await response.json();
  if (response.status !== 200) {
    fail(`request ${path} failed with HTTP ${String(response.status)}: ${JSON.stringify(body)}`);
  }
  return body;
}

const counterFields = [
  "completeWorkspaceScans",
  "markdownContentOpens",
  "markdownContentBytes",
  "sidecarContentOpens",
  "sidecarContentBytes",
  "gitignoreContentOpens",
  "gitignoreContentBytes",
  "activeAssetStreams",
  "maximumActiveAssetStreams",
  "assetStreamBytes"
];

async function counterSnapshot(server) {
  const snapshot = await jsonRequest(server, "/api/gate-e/counters");
  if (
    typeof snapshot !== "object" ||
    snapshot === null ||
    counterFields.some(
      (field) =>
        !(field in snapshot) || !Number.isSafeInteger(snapshot[field]) || snapshot[field] < 0
    )
  ) {
    fail(`Gate E counter endpoint returned an invalid snapshot: ${JSON.stringify(snapshot)}`);
  }
  return snapshot;
}

function counterObservation(before, after) {
  return {
    after,
    before,
    delta: Object.fromEntries(
      counterFields.map((field) => [
        field,
        field === "maximumActiveAssetStreams" ? after[field] : after[field] - before[field]
      ])
    )
  };
}

function requireCounterDelta(observation, expected, label) {
  for (const [field, value] of Object.entries(expected)) {
    if (observation.delta[field] !== value) {
      fail(
        `${label} counter ${field} was ${String(observation.delta[field])}, expected ${String(value)}`
      );
    }
  }
}

function findNavigationDocument(entries, documentPath) {
  if (!Array.isArray(entries)) {
    return undefined;
  }
  for (const entry of entries) {
    if (entry?.kind === "document" && entry.path === documentPath) {
      return entry;
    }
    if (entry?.kind === "directory") {
      const nested = findNavigationDocument(entry.children, documentPath);
      if (nested) {
        return nested;
      }
    }
  }
  return undefined;
}

function documentMetadata(state, documentPath) {
  const entry = findNavigationDocument(state.navigation, documentPath);
  if (
    !entry ||
    typeof entry.documentMetadataRevision !== "string" ||
    typeof entry.reviewMetadataRevision !== "string"
  ) {
    fail(`state has no complete metadata for ${documentPath}`);
  }
  return {
    document: entry.documentMetadataRevision,
    review: entry.reviewMetadataRevision
  };
}

async function replaceFixtureFile(path, bytes, sequence) {
  const temporaryPath = `${path}.gate-e-${String(sequence)}`;
  await writeFile(temporaryPath, bytes);
  await rename(temporaryPath, path);
  return temporaryPath;
}

async function measuredRequest(server, since) {
  const countersBefore = await counterSnapshot(server);
  const request = await stateRequest(server, since);
  const countersAfter = await counterSnapshot(server);
  const processSample = await readProcessSample(server.child.pid);
  return {
    completedNanoseconds: request.completedNanoseconds,
    sample: {
      counters: counterObservation(countersBefore, countersAfter),
      elapsedMs: request.elapsedMs,
      rssKiB: processSample.rssKiB
    },
    state: request.body
  };
}

function latencySummary(samples) {
  return {
    elapsedMs: {
      median: round(median(samples.map((sample) => sample.elapsedMs))),
      worst: Math.max(...samples.map((sample) => sample.elapsedMs))
    },
    rssKiB: {
      median: median(samples.map((sample) => sample.rssKiB)),
      worst: Math.max(...samples.map((sample) => sample.rssKiB))
    },
    samples
  };
}

function idleSummary(samples) {
  return {
    cpuTicksDelta: {
      median: median(samples.map((sample) => sample.cpuTicksDelta)),
      worst: Math.max(...samples.map((sample) => sample.cpuTicksDelta))
    },
    rssAbsoluteDeltaKiB: {
      median: median(samples.map((sample) => Math.abs(sample.rssDeltaKiB))),
      worst: Math.max(...samples.map((sample) => Math.abs(sample.rssDeltaKiB)))
    },
    samples
  };
}

function externalChangeSummary(samples) {
  return {
    rssKiB: {
      median: median(samples.map((sample) => sample.rssKiB)),
      worst: Math.max(...samples.map((sample) => sample.rssKiB))
    },
    samples,
    stateRequestMs: {
      median: round(median(samples.map((sample) => sample.stateRequestMs))),
      worst: Math.max(...samples.map((sample) => sample.stateRequestMs))
    },
    visibleAfterMutationMs: {
      median: round(median(samples.map((sample) => sample.visibleAfterMutationMs))),
      worst: Math.max(...samples.map((sample) => sample.visibleAfterMutationMs))
    }
  };
}

async function coldSamples(binaryPath, fixture, runtimeRoot) {
  const samples = [];
  let warmup;
  for (let index = 0; index <= reportedSampleCount; index += 1) {
    const server = await launchServer(
      binaryPath,
      fixture,
      runtimeRoot,
      `${fixture.name}-cold-${String(index)}`
    );
    try {
      const processSample = await readProcessSample(server.child.pid);
      const counters = await counterSnapshot(server);
      if (
        counters.completeWorkspaceScans !== 1 ||
        counters.markdownContentOpens !== 0 ||
        counters.sidecarContentOpens !== 0 ||
        counters.activeAssetStreams !== 0
      ) {
        fail(`${fixture.name} cold-start counters violated the measurement contract`);
      }
      const sample = {
        counters,
        elapsedMs: server.processReadyMs,
        rssKiB: processSample.rssKiB
      };
      if (index === 0) {
        warmup = sample;
      } else {
        samples.push(sample);
      }
    } finally {
      await stopServer(server);
    }
  }
  return {
    summary: latencySummary(samples),
    warmup
  };
}

async function warmSamples(binaryPath, fixture, runtimeRoot) {
  const server = await launchServer(binaryPath, fixture, runtimeRoot, `${fixture.name}-warm`);
  try {
    let lastRequestCompleted = server.readyNanoseconds;
    const staleSamples = [];
    let staleWarmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      await waitUntil(lastRequestCompleted + staleAgeNanoseconds);
      const measured = await measuredRequest(server);
      if (
        measured.state.status !== "changed" ||
        measured.state.documentCount !== fixture.visibleDocumentCount
      ) {
        fail(`${fixture.name} stale full-state request returned an unexpected payload`);
      }
      requireCounterDelta(
        measured.sample.counters,
        {
          completeWorkspaceScans: 1,
          gitignoreContentOpens: 0,
          markdownContentOpens: 0,
          sidecarContentOpens: 0
        },
        `${fixture.name} stale full-state`
      );
      lastRequestCompleted = measured.completedNanoseconds;
      if (index === 0) {
        staleWarmup = measured.sample;
      } else {
        staleSamples.push(measured.sample);
      }
    }

    const freshSamples = [];
    let freshWarmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      const measured = await measuredRequest(server);
      if (measured.state.status !== "changed") {
        fail(`${fixture.name} fresh full-state request was not complete`);
      }
      requireCounterDelta(
        measured.sample.counters,
        {
          completeWorkspaceScans: 0,
          gitignoreContentOpens: 0,
          markdownContentOpens: 0,
          sidecarContentOpens: 0
        },
        `${fixture.name} fresh full-state`
      );
      lastRequestCompleted = measured.completedNanoseconds;
      if (index === 0) {
        freshWarmup = measured.sample;
      } else {
        freshSamples.push(measured.sample);
      }
    }

    const currentState = await stateRequest(server);
    lastRequestCompleted = currentState.completedNanoseconds;
    const currentRevision = currentState.body.workspaceRevision;
    const unchangedSamples = [];
    let unchangedWarmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      const measured = await measuredRequest(server, currentRevision);
      if (
        measured.state.status !== "unchanged" ||
        measured.state.workspaceRevision !== currentRevision
      ) {
        fail(`${fixture.name} conditional request was not unchanged`);
      }
      requireCounterDelta(
        measured.sample.counters,
        {
          completeWorkspaceScans: 0,
          gitignoreContentOpens: 0,
          markdownContentOpens: 0,
          sidecarContentOpens: 0
        },
        `${fixture.name} unchanged conditional`
      );
      lastRequestCompleted = measured.completedNanoseconds;
      if (index === 0) {
        unchangedWarmup = measured.sample;
      } else {
        unchangedSamples.push(measured.sample);
      }
    }

    const concurrentSamples = [];
    let concurrentWarmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      await waitUntil(lastRequestCompleted + staleAgeNanoseconds);
      const countersBefore = await counterSnapshot(server);
      const startedNanoseconds = process.hrtime.bigint();
      const requests = await Promise.all(
        Array.from({ length: 5 }, async () => stateRequest(server, currentRevision))
      );
      const completedNanoseconds = process.hrtime.bigint();
      const countersAfter = await counterSnapshot(server);
      const counters = counterObservation(countersBefore, countersAfter);
      const revisions = new Set(requests.map((request) => request.body.workspaceRevision));
      if (
        requests.some((request) => request.body.status !== "unchanged") ||
        revisions.size !== 1 ||
        !revisions.has(currentRevision)
      ) {
        fail(`${fixture.name} concurrent conditional requests diverged`);
      }
      requireCounterDelta(
        counters,
        {
          completeWorkspaceScans: 1,
          gitignoreContentOpens: 0,
          markdownContentOpens: 0,
          sidecarContentOpens: 0
        },
        `${fixture.name} concurrent stale conditional`
      );
      lastRequestCompleted = completedNanoseconds;
      const processSample = await readProcessSample(server.child.pid);
      const sample = {
        counters,
        elapsedMs: round(elapsedMilliseconds(startedNanoseconds, completedNanoseconds)),
        requestCount: requests.length,
        rssKiB: processSample.rssKiB
      };
      if (index === 0) {
        concurrentWarmup = sample;
      } else {
        concurrentSamples.push(sample);
      }
    }

    const idleSamples = [];
    let idleWarmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      const before = await readProcessSample(server.child.pid);
      const countersBefore = await counterSnapshot(server);
      const startedNanoseconds = process.hrtime.bigint();
      await waitUntil(startedNanoseconds + idleWindowNanoseconds);
      const completedNanoseconds = process.hrtime.bigint();
      const after = await readProcessSample(server.child.pid);
      const countersAfter = await counterSnapshot(server);
      const counters = counterObservation(countersBefore, countersAfter);
      requireCounterDelta(
        counters,
        {
          completeWorkspaceScans: 0,
          gitignoreContentOpens: 0,
          markdownContentOpens: 0,
          sidecarContentOpens: 0
        },
        `${fixture.name} no-request interval`
      );
      const sample = {
        counters,
        cpuTicksDelta: after.cpuTicks - before.cpuTicks,
        durationMs: round(elapsedMilliseconds(startedNanoseconds, completedNanoseconds)),
        rssDeltaKiB: after.rssKiB - before.rssKiB,
        rssEndKiB: after.rssKiB,
        rssStartKiB: before.rssKiB
      };
      if (index === 0) {
        idleWarmup = sample;
      } else {
        idleSamples.push(sample);
      }
    }

    return {
      concurrentStaleConditional: {
        summary: latencySummary(concurrentSamples),
        warmup: concurrentWarmup
      },
      idleNoRequests: {
        summary: idleSummary(idleSamples),
        warmup: idleWarmup
      },
      unchangedConditional: {
        summary: latencySummary(unchangedSamples),
        warmup: unchangedWarmup
      },
      warmFreshFullState: {
        summary: latencySummary(freshSamples),
        warmup: freshWarmup
      },
      warmStaleFullState: {
        summary: latencySummary(staleSamples),
        warmup: staleWarmup
      }
    };
  } finally {
    await stopServer(server);
  }
}

async function measureExternalChanges(binaryPath, fixture, runtimeRoot) {
  const documentPath = "external.md";
  const absoluteDocumentPath = join(fixture.directory, documentPath);
  const absoluteSidecarPath = `${absoluteDocumentPath}.review.json`;
  const initialDocumentSource = documentSource(fixture.name, 0);
  const initialSidecarSource = emptySidecar();
  const temporaryPaths = [];
  const server = await launchServer(binaryPath, fixture, runtimeRoot, `${fixture.name}-mutations`);

  try {
    const initialState = await stateRequest(server);
    if (initialState.body.status !== "changed") {
      fail("external-change bootstrap state was not complete");
    }
    let currentRevision = initialState.body.workspaceRevision;
    let currentMetadata = documentMetadata(initialState.body, documentPath);
    let lastStateCompleted = initialState.completedNanoseconds;
    let currentDocumentSource = initialDocumentSource;
    let currentSidecarSource = initialSidecarSource;
    let currentSidecarMessage = null;
    let sequence = 0;

    const cases = [
      {
        changesDocument: true,
        changesSidecar: false,
        name: "externalDocumentChange",
        mutate: async () => {
          currentDocumentSource = [
            "# External document change",
            "",
            `Sequence ${String(sequence)}`,
            ""
          ].join("\n");
          temporaryPaths.push(
            await replaceFixtureFile(absoluteDocumentPath, currentDocumentSource, sequence)
          );
        }
      },
      {
        changesDocument: false,
        changesSidecar: true,
        name: "externalSidecarChange",
        mutate: async () => {
          currentSidecarMessage = `External sidecar sequence ${String(sequence)}`;
          currentSidecarSource = sidecarWithMessage(currentSidecarMessage, sequence);
          temporaryPaths.push(
            await replaceFixtureFile(absoluteSidecarPath, currentSidecarSource, sequence)
          );
        }
      },
      {
        changesDocument: true,
        changesSidecar: true,
        name: "simultaneousDocumentAndSidecarChange",
        mutate: async () => {
          currentDocumentSource = [
            "# Simultaneous external change",
            "",
            `Sequence ${String(sequence)}`,
            ""
          ].join("\n");
          currentSidecarMessage = `Simultaneous sidecar sequence ${String(sequence)}`;
          currentSidecarSource = sidecarWithMessage(currentSidecarMessage, sequence);
          temporaryPaths.push(
            await replaceFixtureFile(absoluteDocumentPath, currentDocumentSource, sequence)
          );
          temporaryPaths.push(
            await replaceFixtureFile(absoluteSidecarPath, currentSidecarSource, sequence)
          );
        }
      }
    ];

    const measurements = {};
    for (const measurementCase of cases) {
      const samples = [];
      let warmup;
      for (let index = 0; index <= reportedSampleCount; index += 1) {
        sequence += 1;
        const countersBefore = await counterSnapshot(server);
        const mutationStarted = process.hrtime.bigint();
        await measurementCase.mutate();
        await waitUntil(lastStateCompleted + staleAgeNanoseconds);
        const changed = await stateRequest(server, currentRevision);
        if (
          changed.body.status !== "changed" ||
          changed.body.workspaceRevision <= currentRevision
        ) {
          fail(`${measurementCase.name} did not publish a newer workspace state`);
        }
        const nextMetadata = documentMetadata(changed.body, documentPath);
        if (
          (nextMetadata.document !== currentMetadata.document) !==
            measurementCase.changesDocument ||
          (nextMetadata.review !== currentMetadata.review) !== measurementCase.changesSidecar
        ) {
          fail(`${measurementCase.name} changed unexpected metadata revisions`);
        }

        const [document, review] = await Promise.all([
          jsonRequest(server, `/api/document?path=${encodeURIComponent(documentPath)}`),
          jsonRequest(server, `/api/review?path=${encodeURIComponent(documentPath)}`)
        ]);
        if (
          document.source !== currentDocumentSource ||
          review.documentRevision !== document.revision
        ) {
          fail(`${measurementCase.name} exact document/review pair is incoherent`);
        }
        const observedMessage = review.threads?.[0]?.messages?.[0]?.body ?? null;
        if (observedMessage !== currentSidecarMessage) {
          fail(`${measurementCase.name} exact sidecar content is stale`);
        }

        const countersAfter = await counterSnapshot(server);
        const counters = counterObservation(countersBefore, countersAfter);
        requireCounterDelta(
          counters,
          {
            completeWorkspaceScans: 1,
            gitignoreContentOpens: 0,
            markdownContentOpens: 3,
            sidecarContentOpens: 1
          },
          measurementCase.name
        );
        if (
          counters.delta.markdownContentBytes !== 3 * Buffer.byteLength(currentDocumentSource) ||
          counters.delta.sidecarContentBytes !== Buffer.byteLength(currentSidecarSource)
        ) {
          fail(`${measurementCase.name} content-byte counters did not match exact fixture bytes`);
        }
        const processSample = await readProcessSample(server.child.pid);
        const sample = {
          counters,
          rssKiB: processSample.rssKiB,
          stateRequestMs: changed.elapsedMs,
          visibleAfterMutationMs: round(
            elapsedMilliseconds(mutationStarted, changed.completedNanoseconds)
          )
        };
        currentRevision = changed.body.workspaceRevision;
        currentMetadata = nextMetadata;
        lastStateCompleted = changed.completedNanoseconds;
        if (index === 0) {
          warmup = sample;
        } else {
          samples.push(sample);
        }
      }
      measurements[measurementCase.name] = {
        summary: externalChangeSummary(samples),
        warmup
      };
    }
    return measurements;
  } finally {
    await stopServer(server);
    await writeFile(absoluteDocumentPath, initialDocumentSource, "utf8");
    await writeFile(absoluteSidecarPath, initialSidecarSource, "utf8");
    await Promise.all(
      temporaryPaths.map(async (temporaryPath) => rm(temporaryPath, { force: true }))
    );
  }
}

async function measureFixture(binaryPath, fixture, runtimeRoot) {
  const cold = await coldSamples(binaryPath, fixture, runtimeRoot);
  const warm = await warmSamples(binaryPath, fixture, runtimeRoot);
  return {
    coldProcessReady: cold,
    ...warm
  };
}

async function main() {
  if (process.platform !== "linux") {
    fail("the Gate E backend baseline requires Linux /proc");
  }
  const { binaryPath, outputDirectory, projectDirectory } = parseArguments();
  await mkdir(outputDirectory, { recursive: true });
  const runtimeRoot = await mkdtemp(join(tmpdir(), "mdreview-gate-e-runtime."));
  const startedAt = new Date().toISOString();

  try {
    const fixtures = await generateFixtures(outputDirectory);
    const [environment, checksums] = await Promise.all([
      environmentRecord(projectDirectory, outputDirectory),
      artifactChecksums(projectDirectory, outputDirectory, binaryPath)
    ]);

    const pollingFixtureNames = new Set([
      "workspace-10",
      "workspace-100",
      "workspace-1000",
      "ignored-tree"
    ]);
    const measurements = {};
    for (const fixture of fixtures.fixtures.filter((candidate) =>
      pollingFixtureNames.has(candidate.name)
    )) {
      process.stdout.write(`Measuring ${fixture.name}...\n`);
      measurements[fixture.name] = await measureFixture(binaryPath, fixture, runtimeRoot);
    }
    const externalChangeFixture = fixtures.fixtures.find(
      (fixture) => fixture.name === "external-change"
    );
    if (!externalChangeFixture) {
      fail("external-change fixture was not generated");
    }
    process.stdout.write("Measuring external-change mutations...\n");
    measurements["external-change"] = await measureExternalChanges(
      binaryPath,
      externalChangeFixture,
      runtimeRoot
    );

    const result = {
      checksums: {
        ...checksums,
        fixtureManifest: fixtures.aggregateManifest
      },
      command: "./scripts/gate-e/run-backend-baseline.sh",
      environment,
      fixtureGeneratorVersion: generatorVersion,
      fixtures: fixtures.fixtures.map((fixture) => ({
        fileCount: fixture.fileCount,
        ignoredDocumentCount: fixture.ignoredDocumentCount,
        manifest: fixture.manifest,
        name: fixture.name,
        visibleDocumentCount: fixture.visibleDocumentCount
      })),
      measurementLimits: {
        companionEvidence:
          "The milestone verifier records image fetch, object-URL, retained-blob, and asset-stream measurements in image-baseline.json.",
        observed:
          "HTTP completion time, process-ready time, /proc VmRSS, /proc user-plus-system CPU ticks, and opt-in authenticated workspace/content/asset counters",
        pendingM7: [],
        statement:
          "Every scan/content-read claim is checked against before/after process counters; concurrent requests returning one revision are not used as a substitute."
      },
      measurements,
      reportedSamplesPerCase: reportedSampleCount,
      schemaVersion: 1,
      startedAt
    };
    await writeFile(join(outputDirectory, "backend-baseline.json"), stableJSON(result), "utf8");
  } finally {
    await rm(runtimeRoot, { force: true, recursive: true });
  }
}

await main();
