import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { once } from "node:events";
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { basename, join, relative, resolve, sep } from "node:path";
import process from "node:process";
import { clearTimeout, setTimeout } from "node:timers";

const require = createRequire(import.meta.url);
const generatorVersion = "gate-e-images-v1";
const reportedSampleCount = 5;
const imageSizeBytes = 10 * 1024 * 1024;
const maximumImageAssetBytes = 20 * 1024 * 1024;
const retainedBudgetBytes = 40 * 1024 * 1024;
const expectedImageCount = 5;
const expectedTabCount = 2;
const sampleTimeoutMs = 20_000;
const maximumTransferFramingBytesPerImage = 64 * 1024;
const pngSeed = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACAQMAAABIeJ9nAAAAIGNIUk0AAHomAACAhAAA+gAAAIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAAGUExURU18/v///0yE6jUAAAABYktHRAH/Ai3eAAAAB3RJTUUH6gcdAh8Iq12T1AAAACV0RVh0ZGF0ZTpjcmVhdGUAMjAyNi0wNy0yOVQwMjozMTowOCswMDowMBiH3zMAAAAldEVYdGRhdGU6bW9kaWZ5ADIwMjYtMDctMjlUMDI6MzE6MDgrMDA6MDBp2mePAAAAKHRFWHRkYXRlOnRpbWVzdGFtcAAyMDI2LTA3LTI5VDAyOjMxOjA4KzAwOjAwPs9GUAAAAAxJREFUCNdjYGBgAAAABAABJzQnCgAAAABJRU5ErkJggg==",
  "base64"
);

function fail(message) {
  throw new Error(message);
}

function parseArguments() {
  const [projectDirectoryArgument, outputDirectoryArgument, binaryPathArgument] =
    process.argv.slice(2);
  if (!projectDirectoryArgument || !outputDirectoryArgument || !binaryPathArgument) {
    fail("usage: measure-images.mjs <project-directory> <output-directory> <binary>");
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

async function fileIdentity(path) {
  const bytes = await readFile(path);
  return {
    path: basename(path),
    sha256: sha256(bytes),
    sizeBytes: bytes.length
  };
}

async function pathManifest(rootDirectory, includedPath) {
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
  await visit(join(rootDirectory, includedPath));
  files.sort((left, right) => left.path.localeCompare(right.path, "en"));
  return files;
}

async function createFixture(outputDirectory) {
  const workspaceDirectory = join(outputDirectory, "image-heavy-workspace");
  const assetDirectory = join(workspaceDirectory, "assets");
  await mkdir(assetDirectory, { recursive: true });
  const imageBytes = Buffer.alloc(imageSizeBytes);
  pngSeed.copy(imageBytes);
  const files = [];

  const readme = "# Gate E navigation\n\nNavigate here to release every image resource.\n";
  await writeFile(join(workspaceDirectory, "README.md"), readme, "utf8");
  files.push({
    path: "README.md",
    sha256: sha256(readme),
    sizeBytes: Buffer.byteLength(readme)
  });

  const imageLines = [];
  for (let index = 1; index <= expectedImageCount; index += 1) {
    const name = `image-${String(index)}.png`;
    await writeFile(join(assetDirectory, name), imageBytes);
    files.push({
      path: `assets/${name}`,
      sha256: sha256(imageBytes),
      sizeBytes: imageBytes.length
    });
    imageLines.push(`![Gate E image ${String(index)}](assets/${name})`);
  }
  const unsupported = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>\n';
  await writeFile(join(assetDirectory, "unsupported.svg"), unsupported, "utf8");
  files.push({
    path: "assets/unsupported.svg",
    sha256: sha256(unsupported),
    sizeBytes: Buffer.byteLength(unsupported)
  });
  const oversized = Buffer.alloc(maximumImageAssetBytes + 1);
  pngSeed.copy(oversized);
  await writeFile(join(assetDirectory, "oversized.png"), oversized);
  files.push({
    path: "assets/oversized.png",
    sha256: sha256(oversized),
    sizeBytes: oversized.length
  });
  const outsideSentinel = Buffer.from("outside-workspace-sentinel\n", "utf8");
  await writeFile(join(outputDirectory, "outside-secret.png"), outsideSentinel);
  files.push({
    path: "../outside-secret.png",
    sha256: sha256(outsideSentinel),
    sizeBytes: outsideSentinel.length
  });
  const markdown = [
    "# Gate E image-heavy document",
    "",
    "Five distinct 10 MiB PNG responses exercise four-request concurrency and one LRU eviction.",
    "",
    ...imageLines,
    ""
  ].join("\n");
  await writeFile(join(workspaceDirectory, "image-heavy.md"), markdown, "utf8");
  files.push({
    path: "image-heavy.md",
    sha256: sha256(markdown),
    sizeBytes: Buffer.byteLength(markdown)
  });
  files.sort((left, right) => left.path.localeCompare(right.path, "en"));
  const manifest = {
    files,
    generatorVersion,
    imageCount: expectedImageCount,
    imageSizeBytes,
    validRasterSeedBytes: pngSeed.length
  };
  const manifestBytes = stableJSON(manifest);
  const manifestPath = join(outputDirectory, "image-fixture-manifest.json");
  await writeFile(manifestPath, manifestBytes, "utf8");
  return {
    manifest,
    manifestIdentity: {
      path: basename(manifestPath),
      sha256: sha256(manifestBytes),
      sizeBytes: Buffer.byteLength(manifestBytes)
    },
    workspaceDirectory
  };
}

function waitForStartup(child) {
  return new Promise((resolvePromise, rejectPromise) => {
    let pending = "";
    let output = "";
    let errorOutput = "";
    let instanceURL = "";
    const finish = (callback) => {
      clearTimeout(timeout);
      child.off("exit", handleExit);
      child.stdout.off("data", handleStdout);
      child.stderr.off("data", handleStderr);
      callback();
    };
    const handleExit = (code, signal) => {
      finish(() => {
        rejectPromise(
          new Error(
            `mdReview exited before readiness (code ${String(code)}, signal ${String(signal)})\n${errorOutput}`
          )
        );
      });
    };
    const handleStderr = (chunk) => {
      errorOutput += chunk;
    };
    const handleStdout = (chunk) => {
      output += chunk;
      pending += chunk;
      const lines = pending.split("\n");
      pending = lines.pop() ?? "";
      for (const line of lines) {
        if (line.startsWith("URL:")) {
          instanceURL = line.slice("URL:".length).trim();
        }
        if (line.includes("Press Ctrl+C to stop.")) {
          finish(() => {
            if (instanceURL.length === 0) {
              rejectPromise(new Error(`mdReview printed no startup URL\n${output}`));
              return;
            }
            resolvePromise(instanceURL);
          });
          return;
        }
      }
    };
    const timeout = setTimeout(() => {
      finish(() => {
        rejectPromise(
          new Error(`timed out waiting for mdReview startup\n${output}\n${errorOutput}`)
        );
      });
    }, 10_000);
    child.once("exit", handleExit);
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", handleStdout);
    child.stderr.on("data", handleStderr);
  });
}

async function launchServer(binaryPath, workspaceDirectory, runtimeDirectory) {
  const environment = {
    ...process.env,
    MDREVIEW_GATE_E_COUNTERS: "1",
    PATH: "/mdreview-gate-e-no-executables",
    XDG_RUNTIME_DIR: runtimeDirectory
  };
  const child = spawn(binaryPath, [workspaceDirectory], {
    env: environment,
    stdio: ["ignore", "pipe", "pipe"]
  });
  try {
    const instanceURL = await waitForStartup(child);
    const parsed = new URL(instanceURL);
    if (parsed.search || parsed.hash) {
      fail("mdReview startup URL must not contain a query or fragment");
    }
    return {
      baseURL: parsed.toString().replace(/\/$/u, ""),
      child
    };
  } catch (error) {
    child.kill("SIGKILL");
    throw error;
  }
}

async function stopServer(server) {
  if (server.child.exitCode !== null || server.child.signalCode !== null) {
    return;
  }
  const exit = once(server.child, "exit");
  server.child.kill("SIGINT");
  await Promise.race([
    exit,
    new Promise((resolvePromise) => {
      setTimeout(resolvePromise, 5_000);
    })
  ]);
  if (server.child.exitCode === null && server.child.signalCode === null) {
    const forcedExit = once(server.child, "exit");
    server.child.kill("SIGKILL");
    await forcedExit;
  }
}

async function installObjectURLCounters(page) {
  await page.addInitScript(() => {
    const sizes = new Map();
    const created = [];
    const revoked = [];
    const counters = {
      createdBytes: 0,
      createdCount: 0,
      currentBytes: 0,
      currentCount: 0,
      maximumBytes: 0,
      maximumCount: 0,
      revokedBytes: 0,
      revokedCount: 0
    };
    const createObjectURL = URL.createObjectURL.bind(URL);
    const revokeObjectURL = URL.revokeObjectURL.bind(URL);
    URL.createObjectURL = (blob) => {
      const objectURL = createObjectURL(blob);
      sizes.set(objectURL, blob.size);
      created.push(objectURL);
      counters.createdBytes += blob.size;
      counters.createdCount += 1;
      counters.currentBytes += blob.size;
      counters.currentCount += 1;
      counters.maximumBytes = Math.max(counters.maximumBytes, counters.currentBytes);
      counters.maximumCount = Math.max(counters.maximumCount, counters.currentCount);
      return objectURL;
    };
    URL.revokeObjectURL = (objectURL) => {
      const size = sizes.get(objectURL);
      if (size !== undefined) {
        sizes.delete(objectURL);
        revoked.push(objectURL);
        counters.revokedBytes += size;
        counters.revokedCount += 1;
        counters.currentBytes -= size;
        counters.currentCount -= 1;
      }
      revokeObjectURL(objectURL);
    };
    Reflect.set(globalThis, "__mdreviewGateEImageURLs", {
      counters,
      created,
      revoked
    });
  });
}

async function objectURLSnapshot(page) {
  return page.evaluate(() => {
    const observation = Reflect.get(globalThis, "__mdreviewGateEImageURLs");
    if (!observation || typeof observation !== "object") {
      throw new Error("image URL counters are unavailable");
    }
    return {
      counters: { ...observation.counters },
      created: [...observation.created],
      revoked: [...observation.revoked]
    };
  });
}

async function gateECounterSnapshot(server) {
  const response = await fetch(`${server.baseURL}/api/gate-e/counters`, {
    redirect: "error"
  });
  const snapshot = await response.json();
  const requiredFields = [
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
  if (
    response.status !== 200 ||
    typeof snapshot !== "object" ||
    snapshot === null ||
    requiredFields.some(
      (field) =>
        !(field in snapshot) || !Number.isSafeInteger(snapshot[field]) || snapshot[field] < 0
    )
  ) {
    fail(`Gate E counter endpoint returned an invalid snapshot: ${stableJSON(snapshot).trim()}`);
  }
  return snapshot;
}

function counterDelta(before, after) {
  return Object.fromEntries(
    Object.keys(after).map((field) => [
      field,
      field === "maximumActiveAssetStreams" ? after[field] : after[field] - before[field]
    ])
  );
}

function installNetworkCounters(page, aggregate) {
  const active = new Set();
  const bodyPromises = [];
  const counters = {
    failedResponses: 0,
    maximumActiveFetches: 0,
    successfulBytes: 0,
    successfulResponses: 0
  };
  const isAsset = (request) => new URL(request.url()).pathname === "/api/asset";
  page.on("request", (request) => {
    if (!isAsset(request)) {
      return;
    }
    active.add(request);
    aggregate.active += 1;
    counters.maximumActiveFetches = Math.max(counters.maximumActiveFetches, active.size);
    aggregate.maximumActive = Math.max(aggregate.maximumActive, aggregate.active);
  });
  const finish = (request) => {
    if (!active.delete(request)) {
      return;
    }
    aggregate.active -= 1;
  };
  page.on("requestfinished", (request) => {
    finish(request);
    if (!isAsset(request)) {
      return;
    }
    bodyPromises.push(
      (async () => {
        const response = await request.response();
        if (!response || !response.ok()) {
          counters.failedResponses += 1;
          return;
        }
        const sizes = await request.sizes();
        counters.successfulResponses += 1;
        counters.successfulBytes += sizes.responseBodySize;
      })()
    );
  });
  page.on("requestfailed", (request) => {
    finish(request);
    if (isAsset(request)) {
      counters.failedResponses += 1;
    }
  });
  return {
    active,
    bodyPromises,
    counters
  };
}

async function validateCompiledHTTPRejections(server) {
  const cases = [
    {
      code: "assetUnsupportedType",
      name: "unsupported",
      reference: "assets/unsupported.svg",
      status: 415
    },
    {
      code: "assetTooLarge",
      name: "oversized",
      reference: "assets/oversized.png",
      status: 413
    },
    {
      code: "assetNotFound",
      name: "traversal",
      reference: "../outside-secret.png",
      status: 404
    }
  ];
  const results = [];
  for (const testCase of cases) {
    const url = new URL("/api/asset", server.baseURL);
    url.searchParams.set("documentPath", "image-heavy.md");
    url.searchParams.set("reference", testCase.reference);
    const response = await fetch(url, { redirect: "error" });
    const body = await response.json();
    const code =
      typeof body === "object" &&
      body !== null &&
      "error" in body &&
      typeof body.error === "object" &&
      body.error !== null &&
      "code" in body.error &&
      typeof body.error.code === "string"
        ? body.error.code
        : undefined;
    if (response.status !== testCase.status || code !== testCase.code) {
      fail(
        `${testCase.name} compiled HTTP rejection was ${String(response.status)} ${String(code)}, ` +
          `not ${String(testCase.status)} ${testCase.code}`
      );
    }
    results.push({
      code,
      name: testCase.name,
      reference: testCase.reference,
      status: response.status
    });
  }
  return results;
}

async function waitFor(predicate, label, timeoutMs = sampleTimeoutMs) {
  const started = Date.now();
  while (!(await predicate())) {
    if (Date.now() - started > timeoutMs) {
      fail(`timed out waiting for ${label}`);
    }
    await new Promise((resolvePromise) => {
      setTimeout(resolvePromise, 25);
    });
  }
}

function validateLoadedURLs(snapshot, tabLabel) {
  const { counters, created, revoked } = snapshot;
  if (
    counters.createdCount !== expectedImageCount ||
    counters.createdBytes !== expectedImageCount * imageSizeBytes ||
    counters.revokedCount !== 1 ||
    counters.revokedBytes !== imageSizeBytes ||
    counters.currentCount !== 4 ||
    counters.currentBytes !== retainedBudgetBytes ||
    counters.maximumCount !== 4 ||
    counters.maximumBytes !== retainedBudgetBytes
  ) {
    fail(`${tabLabel} object URL accounting violated the 40 MiB LRU contract`);
  }
  if (revoked[0] !== created[0]) {
    fail(`${tabLabel} did not revoke the least-recently-admitted object URL`);
  }
}

function validateCleanedURLs(snapshot, tabLabel) {
  const { counters, created, revoked } = snapshot;
  if (
    counters.createdCount !== expectedImageCount ||
    counters.revokedCount !== expectedImageCount ||
    counters.revokedBytes !== counters.createdBytes ||
    counters.currentCount !== 0 ||
    counters.currentBytes !== 0 ||
    new Set(created).size !== expectedImageCount ||
    new Set(revoked).size !== expectedImageCount
  ) {
    fail(`${tabLabel} retained or double-counted an object URL after navigation`);
  }
}

async function openMeasurementPage(context, server, tabIndex, aggregate) {
  const page = await context.newPage();
  await installObjectURLCounters(page);
  const network = installNetworkCounters(page, aggregate);
  await page.goto(`${server.baseURL}/`);
  await page
    .getByRole("heading", { level: 1, name: "Gate E navigation" })
    .waitFor({ state: "visible", timeout: sampleTimeoutMs });
  return {
    network,
    page,
    tabLabel: `tab-${String(tabIndex + 1)}`
  };
}

async function measureSample(browser, server, sampleIndex) {
  const aggregate = {
    active: 0,
    maximumActive: 0
  };
  const contexts = [];
  try {
    for (let index = 0; index < expectedTabCount; index += 1) {
      contexts.push(
        await browser.newContext({
          viewport: {
            height: 900,
            width: 1280
          }
        })
      );
    }
    const tabs = await Promise.all(
      contexts.map((context, index) => openMeasurementPage(context, server, index, aggregate))
    );
    const startedNanoseconds = process.hrtime.bigint();
    await Promise.all(
      tabs.map(async ({ page }) => {
        await page.getByRole("button", { name: "image-heavy.md", exact: true }).click();
        await page
          .getByRole("heading", { level: 1, name: "Gate E image-heavy document" })
          .waitFor({ state: "visible", timeout: sampleTimeoutMs });
      })
    );

    await Promise.all(
      tabs.map(async ({ page, tabLabel }) => {
        await waitFor(async () => {
          const snapshot = await objectURLSnapshot(page);
          return (
            snapshot.counters.createdCount === expectedImageCount &&
            snapshot.counters.currentBytes === retainedBudgetBytes &&
            snapshot.counters.revokedCount === 1
          );
        }, `${tabLabel} image admission`);
        await page.locator(".markdown-image img").first().waitFor({
          state: "visible",
          timeout: sampleTimeoutMs
        });
        await waitFor(
          () =>
            page
              .locator(".markdown-image img")
              .evaluateAll(
                (images) =>
                  images.length === 4 &&
                  images.every(
                    (image) =>
                      image instanceof HTMLImageElement &&
                      image.complete &&
                      image.naturalWidth > 0 &&
                      image.naturalHeight > 0
                  )
              ),
          `${tabLabel} retained raster decode`
        );
      })
    );
    await waitFor(
      () =>
        Promise.resolve(
          aggregate.active === 0 && tabs.every(({ network }) => network.active.size === 0)
        ),
      "asset request completion"
    );
    await Promise.all(tabs.flatMap(({ network }) => network.bodyPromises));
    const completedNanoseconds = process.hrtime.bigint();
    const loadedSnapshots = await Promise.all(tabs.map(({ page }) => objectURLSnapshot(page)));

    for (let index = 0; index < tabs.length; index += 1) {
      const tab = tabs[index];
      const snapshot = loadedSnapshots[index];
      validateLoadedURLs(snapshot, tab.tabLabel);
      const expectedPayloadBytes = expectedImageCount * imageSizeBytes;
      const maximumTransferredBytes =
        expectedPayloadBytes + expectedImageCount * maximumTransferFramingBytesPerImage;
      if (
        tab.network.counters.maximumActiveFetches !== 4 ||
        tab.network.counters.successfulResponses !== expectedImageCount ||
        tab.network.counters.successfulBytes < expectedPayloadBytes ||
        tab.network.counters.successfulBytes > maximumTransferredBytes ||
        tab.network.counters.failedResponses !== 0
      ) {
        fail(
          `${tab.tabLabel} asset request counters violated the measurement contract: ` +
            stableJSON(tab.network.counters).trim()
        );
      }
    }
    if (aggregate.maximumActive !== 8) {
      fail(`two-tab aggregate browser fetch maximum was ${String(aggregate.maximumActive)}, not 8`);
    }

    await Promise.all(
      tabs.map(async ({ page }) => {
        await page.getByRole("button", { name: "README.md", exact: true }).click();
        await page
          .getByRole("heading", { level: 1, name: "Gate E navigation" })
          .waitFor({ state: "visible", timeout: sampleTimeoutMs });
      })
    );
    await Promise.all(
      tabs.map(async ({ page, tabLabel }) => {
        await waitFor(
          async () => (await objectURLSnapshot(page)).counters.currentBytes === 0,
          `${tabLabel} navigation cleanup`
        );
      })
    );
    const cleanedSnapshots = await Promise.all(tabs.map(({ page }) => objectURLSnapshot(page)));
    for (let index = 0; index < tabs.length; index += 1) {
      validateCleanedURLs(cleanedSnapshots[index], tabs[index].tabLabel);
    }

    return {
      aggregateMaximumActiveFetches: aggregate.maximumActive,
      elapsedMs: round(Number(completedNanoseconds - startedNanoseconds) / 1_000_000),
      sampleIndex,
      successfulAssetBytes: tabs.reduce(
        (total, { network }) => total + network.counters.successfulBytes,
        0
      ),
      successfulAssetResponses: tabs.reduce(
        (total, { network }) => total + network.counters.successfulResponses,
        0
      ),
      tabs: tabs.map(({ network, tabLabel }, index) => ({
        cleanup: cleanedSnapshots[index].counters,
        loaded: loadedSnapshots[index],
        maximumActiveFetches: network.counters.maximumActiveFetches,
        successfulAssetBytes: network.counters.successfulBytes,
        successfulAssetResponses: network.counters.successfulResponses,
        tab: tabLabel
      }))
    };
  } finally {
    await Promise.allSettled(contexts.map((context) => context.close()));
  }
}

function measurementSummary(samples) {
  return {
    aggregateMaximumActiveFetches: {
      median: median(samples.map((sample) => sample.aggregateMaximumActiveFetches)),
      worst: Math.max(...samples.map((sample) => sample.aggregateMaximumActiveFetches))
    },
    elapsedMs: {
      median: round(median(samples.map((sample) => sample.elapsedMs))),
      worst: Math.max(...samples.map((sample) => sample.elapsedMs))
    },
    maximumRetainedBytesPerTab: {
      median: median(
        samples.flatMap((sample) => sample.tabs.map((tab) => tab.loaded.counters.maximumBytes))
      ),
      worst: Math.max(
        ...samples.flatMap((sample) => sample.tabs.map((tab) => tab.loaded.counters.maximumBytes))
      )
    },
    perTabMaximumActiveFetches: {
      median: median(
        samples.flatMap((sample) => sample.tabs.map((tab) => tab.maximumActiveFetches))
      ),
      worst: Math.max(
        ...samples.flatMap((sample) => sample.tabs.map((tab) => tab.maximumActiveFetches))
      )
    },
    samples
  };
}

async function environmentRecord(projectDirectory, fixtureDirectory, browser) {
  const playwrightPackage = JSON.parse(
    await readFile(
      join(projectDirectory, "web", "node_modules", "playwright", "package.json"),
      "utf8"
    )
  );
  return {
    chromium: browser.version(),
    cpu: commandOutput("sh", [
      "-c",
      "awk -F: '/model name/{sub(/^[[:space:]]+/,\"\",$2); print $2; exit}' /proc/cpuinfo"
    ]),
    distribution: commandOutput("sh", [
      "-c",
      '. /etc/os-release && printf \'%s %s\' "$NAME" "$VERSION_ID"'
    ]),
    fixtureFilesystem: commandOutput("findmnt", [
      "-T",
      fixtureDirectory,
      "-n",
      "-o",
      "FSTYPE,SOURCE"
    ]),
    go: commandOutput("go", ["version"]),
    installedMemoryKiB: commandOutput("sh", ["-c", "awk '/MemTotal/{print $2}' /proc/meminfo"]),
    kernel: commandOutput("uname", ["-srmo"]),
    logicalCPUs: commandOutput("getconf", ["_NPROCESSORS_ONLN"]),
    node: process.version,
    npm: commandOutput("npm", ["--version"]),
    playwright: playwrightPackage.version
  };
}

async function main() {
  const { binaryPath, outputDirectory, projectDirectory } = parseArguments();
  await mkdir(outputDirectory, { recursive: true });
  await chmod(outputDirectory, 0o700);
  const runtimeDirectory = await mkdtemp(join(tmpdir(), "mdreview-gate-e-images-runtime."));
  const fixture = await createFixture(outputDirectory);
  const { chromium } = require(join(projectDirectory, "web", "node_modules", "playwright"));
  const browser = await chromium.launch({ headless: true });
  const server = await launchServer(binaryPath, fixture.workspaceDirectory, runtimeDirectory);
  const startedAt = new Date().toISOString();
  try {
    const countersBeforeRejections = await gateECounterSnapshot(server);
    const compiledHTTPRejections = await validateCompiledHTTPRejections(server);
    const countersAfterRejections = await gateECounterSnapshot(server);
    const measured = [];
    let warmup;
    for (let index = 0; index <= reportedSampleCount; index += 1) {
      const countersBefore = await gateECounterSnapshot(server);
      const sample = await measureSample(browser, server, index);
      const countersAfter = await gateECounterSnapshot(server);
      const counters = counterDelta(countersBefore, countersAfter);
      const expectedAssetBytes = expectedTabCount * expectedImageCount * imageSizeBytes;
      if (
        counters.assetStreamBytes !== expectedAssetBytes ||
        countersAfter.activeAssetStreams !== 0 ||
        countersAfter.maximumActiveAssetStreams > 8
      ) {
        fail(
          `server asset-stream counters violated the measurement contract: ${stableJSON({
            after: countersAfter,
            before: countersBefore,
            delta: counters
          }).trim()}`
        );
      }
      sample.serverCounters = {
        after: countersAfter,
        before: countersBefore,
        delta: counters
      };
      if (index === 0) {
        warmup = sample;
      } else {
        measured.push(sample);
      }
    }
    const scriptPaths = [
      "scripts/gate-e/measure-images.mjs",
      "scripts/gate-e/run-image-baseline.sh",
      "scripts/gate-e/verify-resource-counters.sh"
    ];
    const scriptManifest = await pathManifest(projectDirectory, "scripts/gate-e");
    const report = {
      artifactIdentity: {
        binary: await fileIdentity(binaryPath),
        frontendAssets: {
          files: await pathManifest(projectDirectory, "web/dist"),
          manifestSha256: sha256(stableJSON(await pathManifest(projectDirectory, "web/dist")))
        },
        measurementScripts: {
          includedPaths: scriptPaths,
          manifestSha256: sha256(
            stableJSON(scriptManifest.filter((entry) => scriptPaths.includes(entry.path)))
          )
        }
      },
      completedAt: new Date().toISOString(),
      compiledHTTPRejections: {
        cases: compiledHTTPRejections,
        counters: {
          after: countersAfterRejections,
          before: countersBeforeRejections,
          delta: counterDelta(countersBeforeRejections, countersAfterRejections)
        }
      },
      environment: await environmentRecord(projectDirectory, fixture.workspaceDirectory, browser),
      fixture: {
        ...fixture.manifestIdentity,
        details: fixture.manifest
      },
      generatorVersion,
      measured: measurementSummary(measured),
      samplePolicy: {
        reportedSamples: reportedSampleCount,
        warmupSamples: 1
      },
      startedAt,
      warmup
    };
    await writeFile(join(outputDirectory, "image-baseline.json"), stableJSON(report), "utf8");
    process.stdout.write(`${stableJSON({ output: join(outputDirectory, "image-baseline.json") })}`);
  } finally {
    await stopServer(server);
    await browser.close();
    await rm(runtimeDirectory, { force: true, recursive: true });
  }
}

await main();
