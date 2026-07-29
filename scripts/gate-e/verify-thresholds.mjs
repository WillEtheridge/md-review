import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import process from "node:process";

const reportedSamples = 5;
const maximumRSSKiB = 65_536;
const maximumRetainedBytes = 40 * 1024 * 1024;
const exactImageBytes = 100 * 1024 * 1024;
const imageBytes = 10 * 1024 * 1024;
const maximumTransferFramingBytes = 64 * 1024;

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

const expectedBackendFixtures = {
  "workspace-10": {
    fileCount: 20,
    ignoredDocumentCount: 0,
    manifestSha256: "4d59e6efee8c558c4154e38779eb7ffb02d1ff0d1b64624f6e5f25a1a35afd68",
    visibleDocumentCount: 10
  },
  "workspace-100": {
    fileCount: 200,
    ignoredDocumentCount: 0,
    manifestSha256: "ccae72c8ec61045021c03bdf2bf66a7f862d2b3fa2c2b6aeb89fece371bf43ce",
    visibleDocumentCount: 100
  },
  "workspace-1000": {
    fileCount: 2000,
    ignoredDocumentCount: 0,
    manifestSha256: "6283a1bcebdaf9cb46927f5d81d802836bd6117062bfd05e133a3e9f242edbc4",
    visibleDocumentCount: 1000
  },
  "ignored-tree": {
    fileCount: 5201,
    ignoredDocumentCount: 5000,
    manifestSha256: "225b6daf97e6ca0bc4c7248f1657f3b51ee345ec44078fccbc0cb4fcb5e51a21",
    visibleDocumentCount: 100
  },
  "image-heavy": {
    fileCount: 5,
    ignoredDocumentCount: 0,
    manifestSha256: "9b56b484e48aa2e0475d72cdc7afe69d9fadf8cbc28159bce650cf3804e7fd23",
    visibleDocumentCount: 1
  },
  "external-change": {
    fileCount: 2,
    ignoredDocumentCount: 0,
    manifestSha256: "e67805d0db169a6a36b831235ce0b4121b5a37e0cdd1703af957427c9d09d363",
    visibleDocumentCount: 1
  }
};

function fail(label, detail) {
  throw new Error(`${label}: ${detail}`);
}

function requireCondition(condition, label, detail) {
  if (!condition) {
    fail(label, detail);
  }
}

function requireObject(value, label) {
  requireCondition(
    typeof value === "object" && value !== null && !Array.isArray(value),
    label,
    "expected an object"
  );
  return value;
}

function requireArray(value, label, length) {
  requireCondition(Array.isArray(value), label, "expected an array");
  if (length !== undefined) {
    requireCondition(value.length === length, label, `expected ${String(length)} entries`);
  }
  return value;
}

function requireNumber(value, label) {
  requireCondition(Number.isFinite(value), label, "expected a finite number");
  return value;
}

function requireInteger(value, label) {
  requireCondition(Number.isSafeInteger(value), label, "expected a safe integer");
  return value;
}

function requireText(value, label) {
  requireCondition(typeof value === "string" && value.length > 0, label, "expected text");
  requireCondition(!value.startsWith("unavailable"), label, "environment value is unavailable");
  return value;
}

function requireEqual(actual, expected, label) {
  requireCondition(
    Object.is(actual, expected),
    label,
    `got ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`
  );
}

function requireMaximum(actual, maximum, label) {
  requireCondition(actual <= maximum, label, `${String(actual)} exceeds ${String(maximum)}`);
}

function median(values) {
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.floor(sorted.length / 2)];
}

function requireSummary(summaryValue, samples, field, label) {
  const summary = requireObject(summaryValue, label);
  const values = samples.map((sample, index) =>
    requireNumber(
      requireObject(sample, `${label}.samples[${String(index)}]`)[field],
      `${label}.${field}`
    )
  );
  requireEqual(summary.median, median(values), `${label}.median`);
  requireEqual(summary.worst, Math.max(...values), `${label}.worst`);
}

function requireRSS(value, label) {
  requireMaximum(requireInteger(value, label), maximumRSSKiB, label);
}

function requireCounterSnapshot(value, label) {
  const snapshot = requireObject(value, label);
  for (const field of counterFields) {
    const counter = requireInteger(snapshot[field], `${label}.${field}`);
    requireCondition(counter >= 0, `${label}.${field}`, "counter is negative");
  }
  return snapshot;
}

function zeroCounters(overrides = {}) {
  return Object.fromEntries(counterFields.map((field) => [field, overrides[field] ?? 0]));
}

function requireCounterValues(value, expected, label) {
  const counters = requireCounterSnapshot(value, label);
  for (const [field, expectedValue] of Object.entries(expected)) {
    requireEqual(counters[field], expectedValue, `${label}.${field}`);
  }
  return counters;
}

function requireCounterObservation(value, expectedDelta, label) {
  const observation = requireObject(value, label);
  const before = requireCounterSnapshot(observation.before, `${label}.before`);
  const after = requireCounterSnapshot(observation.after, `${label}.after`);
  const delta = requireCounterSnapshot(observation.delta, `${label}.delta`);
  for (const field of counterFields) {
    const calculated =
      field === "maximumActiveAssetStreams" ? after[field] : after[field] - before[field];
    requireEqual(delta[field], calculated, `${label}.delta.${field}`);
  }
  for (const [field, expectedValue] of Object.entries(expectedDelta)) {
    requireEqual(delta[field], expectedValue, `${label}.delta.${field}`);
  }
  requireCondition(
    after.maximumActiveAssetStreams >= before.maximumActiveAssetStreams,
    `${label}.maximumActiveAssetStreams`,
    "maximum gauge decreased"
  );
  return observation;
}

function requireEnvironmentValue(environment, field, label) {
  return requireText(environment[field], `${label}.${field}`);
}

function verifyBackendEnvironment(value) {
  const environment = requireObject(value, "backend.environment");
  for (const field of ["architecture", "distribution", "go", "kernel", "node", "npm"]) {
    requireEnvironmentValue(environment, field, "backend.environment");
  }
  requireCondition(
    environment.go.includes("go1.26.5"),
    "backend.environment.go",
    "expected Go 1.26.5"
  );
  requireEqual(environment.node, "v26.2.0", "backend.environment.node");
  requireEqual(environment.npm, "11.13.0", "backend.environment.npm");
  const browsers = requireObject(environment.browsers, "backend.environment.browsers");
  requireCondition(
    requireText(browsers.playwright, "backend.environment.browsers.playwright").includes("1.62.0"),
    "backend.environment.browsers.playwright",
    "expected Playwright 1.62.0"
  );
  requireText(browsers.chromium, "backend.environment.browsers.chromium");
  requireText(browsers.firefox, "backend.environment.browsers.firefox");
  const cpu = requireObject(environment.cpu, "backend.environment.cpu");
  requireText(cpu.model, "backend.environment.cpu.model");
  requireCondition(
    requireInteger(cpu.logicalCount, "backend.environment.cpu.logicalCount") > 0,
    "backend.environment.cpu.logicalCount",
    "must be positive"
  );
  requireCondition(
    requireInteger(environment.installedMemoryKiB, "backend.environment.installedMemoryKiB") > 0,
    "backend.environment.installedMemoryKiB",
    "must be positive"
  );
  const filesystem = requireObject(environment.filesystem, "backend.environment.filesystem");
  requireText(filesystem.type, "backend.environment.filesystem.type");
  requireText(filesystem.classification, "backend.environment.filesystem.classification");
}

function verifyBackendFixtures(report) {
  requireEqual(
    report.fixtureGeneratorVersion,
    "gate-e-backend-v1",
    "backend.fixtureGeneratorVersion"
  );
  const fixtures = requireArray(
    report.fixtures,
    "backend.fixtures",
    Object.keys(expectedBackendFixtures).length
  );
  const seen = new Set();
  for (const [index, fixtureValue] of fixtures.entries()) {
    const label = `backend.fixtures[${String(index)}]`;
    const fixture = requireObject(fixtureValue, label);
    const name = requireText(fixture.name, `${label}.name`);
    requireCondition(!seen.has(name), label, `duplicate fixture ${name}`);
    seen.add(name);
    const expected = expectedBackendFixtures[name];
    requireCondition(expected !== undefined, label, `unexpected fixture ${name}`);
    requireEqual(fixture.fileCount, expected.fileCount, `${label}.fileCount`);
    requireEqual(
      fixture.ignoredDocumentCount,
      expected.ignoredDocumentCount,
      `${label}.ignoredDocumentCount`
    );
    requireEqual(
      fixture.visibleDocumentCount,
      expected.visibleDocumentCount,
      `${label}.visibleDocumentCount`
    );
    requireEqual(
      requireObject(fixture.manifest, `${label}.manifest`).sha256,
      expected.manifestSha256,
      `${label}.manifest.sha256`
    );
  }
  requireEqual(
    requireObject(
      requireObject(report.checksums, "backend.checksums").fixtureManifest,
      "backend.checksums.fixtureManifest"
    ).sha256,
    "aaf2b64bcc221c96f5c4adac680996a46bbd21f73d4bafbff093a1cafcca3e1e",
    "backend.checksums.fixtureManifest.sha256"
  );
}

function requireArtifactIdentity(value, label) {
  const identity = requireObject(value, label);
  requireCondition(
    typeof identity.sha256 === "string" && /^[0-9a-f]{64}$/u.test(identity.sha256),
    `${label}.sha256`,
    "expected a SHA-256 digest"
  );
  requireCondition(
    requireInteger(identity.sizeBytes, `${label}.sizeBytes`) > 0,
    `${label}.sizeBytes`,
    "must be positive"
  );
  return identity;
}

function backendCaseSamples(value, label) {
  const measurement = requireObject(value, label);
  requireObject(measurement.warmup, `${label}.warmup`);
  const summary = requireObject(measurement.summary, `${label}.summary`);
  const samples = requireArray(summary.samples, `${label}.summary.samples`, reportedSamples);
  requireSummary(summary.elapsedMs, samples, "elapsedMs", `${label}.summary.elapsedMs`);
  requireSummary(summary.rssKiB, samples, "rssKiB", `${label}.summary.rssKiB`);
  for (const [index, sampleValue] of samples.entries()) {
    requireRSS(
      requireObject(sampleValue, `${label}.summary.samples[${String(index)}]`).rssKiB,
      `${label}.summary.samples[${String(index)}].rssKiB`
    );
  }
  return samples;
}

function verifyBackendLatencyCase(value, label, elapsedLimitMs, expectedCounters, extraCheck) {
  const samples = backendCaseSamples(value, label);
  for (const [index, sampleValue] of samples.entries()) {
    const sampleLabel = `${label}.summary.samples[${String(index)}]`;
    const sample = requireObject(sampleValue, sampleLabel);
    requireMaximum(
      requireNumber(sample.elapsedMs, `${sampleLabel}.elapsedMs`),
      elapsedLimitMs,
      `${sampleLabel}.elapsedMs`
    );
    requireCounterObservation(sample.counters, expectedCounters, `${sampleLabel}.counters`);
    extraCheck?.(sample, sampleLabel);
  }
}

function verifyBackendColdCase(value, fixtureName) {
  const label = `backend.measurements.${fixtureName}.coldProcessReady`;
  const samples = backendCaseSamples(value, label);
  for (const [index, sampleValue] of samples.entries()) {
    const sampleLabel = `${label}.summary.samples[${String(index)}]`;
    const sample = requireObject(sampleValue, sampleLabel);
    requireMaximum(
      requireNumber(sample.elapsedMs, `${sampleLabel}.elapsedMs`),
      250,
      `${sampleLabel}.elapsedMs`
    );
    const ignoreOpens = fixtureName === "ignored-tree" ? 1 : 0;
    const ignoreBytes = fixtureName === "ignored-tree" ? 9 : 0;
    requireCounterValues(
      sample.counters,
      zeroCounters({
        completeWorkspaceScans: 1,
        gitignoreContentBytes: ignoreBytes,
        gitignoreContentOpens: ignoreOpens
      }),
      `${sampleLabel}.counters`
    );
  }
}

function verifyBackendIdleCase(value, fixtureName) {
  const label = `backend.measurements.${fixtureName}.idleNoRequests`;
  const measurement = requireObject(value, label);
  requireObject(measurement.warmup, `${label}.warmup`);
  const summary = requireObject(measurement.summary, `${label}.summary`);
  const samples = requireArray(summary.samples, `${label}.summary.samples`, reportedSamples);
  const cpuDeltas = [];
  const rssAbsoluteDeltas = [];
  for (const [index, sampleValue] of samples.entries()) {
    const sampleLabel = `${label}.summary.samples[${String(index)}]`;
    const sample = requireObject(sampleValue, sampleLabel);
    const cpuDelta = requireInteger(sample.cpuTicksDelta, `${sampleLabel}.cpuTicksDelta`);
    const rssDelta = requireInteger(sample.rssDeltaKiB, `${sampleLabel}.rssDeltaKiB`);
    const rssStart = requireInteger(sample.rssStartKiB, `${sampleLabel}.rssStartKiB`);
    const rssEnd = requireInteger(sample.rssEndKiB, `${sampleLabel}.rssEndKiB`);
    requireEqual(rssEnd - rssStart, rssDelta, `${sampleLabel}.rssDeltaKiB`);
    requireRSS(rssStart, `${sampleLabel}.rssStartKiB`);
    requireRSS(rssEnd, `${sampleLabel}.rssEndKiB`);
    requireCondition(
      requireNumber(sample.durationMs, `${sampleLabel}.durationMs`) >= 1000,
      `${sampleLabel}.durationMs`,
      "idle interval was shorter than one second"
    );
    requireCounterObservation(sample.counters, zeroCounters(), `${sampleLabel}.counters`);
    cpuDeltas.push(cpuDelta);
    rssAbsoluteDeltas.push(Math.abs(rssDelta));
  }
  const cpuSummary = requireObject(summary.cpuTicksDelta, `${label}.summary.cpuTicksDelta`);
  requireEqual(cpuSummary.median, median(cpuDeltas), `${label}.summary.cpuTicksDelta.median`);
  requireEqual(cpuSummary.worst, Math.max(...cpuDeltas), `${label}.summary.cpuTicksDelta.worst`);
  requireEqual(cpuSummary.median, 0, `${label}.summary.cpuTicksDelta.median`);
  requireMaximum(cpuSummary.worst, 1, `${label}.summary.cpuTicksDelta.worst`);
  const rssSummary = requireObject(
    summary.rssAbsoluteDeltaKiB,
    `${label}.summary.rssAbsoluteDeltaKiB`
  );
  requireEqual(
    rssSummary.median,
    median(rssAbsoluteDeltas),
    `${label}.summary.rssAbsoluteDeltaKiB.median`
  );
  requireEqual(
    rssSummary.worst,
    Math.max(...rssAbsoluteDeltas),
    `${label}.summary.rssAbsoluteDeltaKiB.worst`
  );
  requireEqual(rssSummary.median, 0, `${label}.summary.rssAbsoluteDeltaKiB.median`);
  requireMaximum(rssSummary.worst, 256, `${label}.summary.rssAbsoluteDeltaKiB.worst`);
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
          anchor: { type: "document" },
          status: "open",
          messages: [
            {
              id: `message_gate_e_${String(sequence)}`,
              author: { type: "agent", name: "Gate E fixture" },
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

function externalExpectedBytes(caseName, sampleIndex) {
  if (caseName === "externalDocumentChange") {
    const sequence = sampleIndex + 2;
    const document = `# External document change\n\nSequence ${String(sequence)}\n`;
    return { document: Buffer.byteLength(document), sidecar: Buffer.byteLength(emptySidecar()) };
  }
  if (caseName === "externalSidecarChange") {
    const sequence = sampleIndex + 8;
    const document = "# External document change\n\nSequence 6\n";
    const sidecar = sidecarWithMessage(`External sidecar sequence ${String(sequence)}`, sequence);
    return { document: Buffer.byteLength(document), sidecar: Buffer.byteLength(sidecar) };
  }
  const sequence = sampleIndex + 14;
  const document = `# Simultaneous external change\n\nSequence ${String(sequence)}\n`;
  const sidecar = sidecarWithMessage(`Simultaneous sidecar sequence ${String(sequence)}`, sequence);
  return { document: Buffer.byteLength(document), sidecar: Buffer.byteLength(sidecar) };
}

function verifyExternalChangeCase(value, caseName) {
  const label = `backend.measurements.external-change.${caseName}`;
  const measurement = requireObject(value, label);
  requireObject(measurement.warmup, `${label}.warmup`);
  const summary = requireObject(measurement.summary, `${label}.summary`);
  const samples = requireArray(summary.samples, `${label}.summary.samples`, reportedSamples);
  requireSummary(
    summary.stateRequestMs,
    samples,
    "stateRequestMs",
    `${label}.summary.stateRequestMs`
  );
  requireSummary(
    summary.visibleAfterMutationMs,
    samples,
    "visibleAfterMutationMs",
    `${label}.summary.visibleAfterMutationMs`
  );
  requireSummary(summary.rssKiB, samples, "rssKiB", `${label}.summary.rssKiB`);
  for (const [index, sampleValue] of samples.entries()) {
    const sampleLabel = `${label}.summary.samples[${String(index)}]`;
    const sample = requireObject(sampleValue, sampleLabel);
    requireMaximum(
      requireNumber(sample.stateRequestMs, `${sampleLabel}.stateRequestMs`),
      100,
      `${sampleLabel}.stateRequestMs`
    );
    requireMaximum(
      requireNumber(sample.visibleAfterMutationMs, `${sampleLabel}.visibleAfterMutationMs`),
      1500,
      `${sampleLabel}.visibleAfterMutationMs`
    );
    requireRSS(sample.rssKiB, `${sampleLabel}.rssKiB`);
    const expectedBytes = externalExpectedBytes(caseName, index);
    requireCounterObservation(
      sample.counters,
      zeroCounters({
        completeWorkspaceScans: 1,
        markdownContentBytes: expectedBytes.document * 3,
        markdownContentOpens: 3,
        sidecarContentBytes: expectedBytes.sidecar,
        sidecarContentOpens: 1
      }),
      `${sampleLabel}.counters`
    );
  }
}

export function verifyBackendReport(reportValue) {
  const report = requireObject(reportValue, "backend");
  requireEqual(report.schemaVersion, 1, "backend.schemaVersion");
  requireEqual(report.reportedSamplesPerCase, reportedSamples, "backend.reportedSamplesPerCase");
  verifyBackendEnvironment(report.environment);
  requireArtifactIdentity(
    requireObject(report.checksums, "backend.checksums").binary,
    "backend.checksums.binary"
  );
  verifyBackendFixtures(report);
  const limits = requireObject(report.measurementLimits, "backend.measurementLimits");
  requireArray(limits.pendingM7, "backend.measurementLimits.pendingM7", 0);
  const measurements = requireObject(report.measurements, "backend.measurements");
  for (const fixtureName of ["workspace-10", "workspace-100", "workspace-1000", "ignored-tree"]) {
    const fixture = requireObject(measurements[fixtureName], `backend.measurements.${fixtureName}`);
    verifyBackendColdCase(fixture.coldProcessReady, fixtureName);
    verifyBackendLatencyCase(
      fixture.warmStaleFullState,
      `backend.measurements.${fixtureName}.warmStaleFullState`,
      100,
      zeroCounters({ completeWorkspaceScans: 1 })
    );
    verifyBackendLatencyCase(
      fixture.warmFreshFullState,
      `backend.measurements.${fixtureName}.warmFreshFullState`,
      50,
      zeroCounters()
    );
    verifyBackendLatencyCase(
      fixture.unchangedConditional,
      `backend.measurements.${fixtureName}.unchangedConditional`,
      20,
      zeroCounters()
    );
    verifyBackendLatencyCase(
      fixture.concurrentStaleConditional,
      `backend.measurements.${fixtureName}.concurrentStaleConditional`,
      150,
      zeroCounters({ completeWorkspaceScans: 1 }),
      (sample, label) => requireEqual(sample.requestCount, 5, `${label}.requestCount`)
    );
    verifyBackendIdleCase(fixture.idleNoRequests, fixtureName);
  }
  const external = requireObject(
    measurements["external-change"],
    "backend.measurements.external-change"
  );
  for (const caseName of [
    "externalDocumentChange",
    "externalSidecarChange",
    "simultaneousDocumentAndSidecarChange"
  ]) {
    verifyExternalChangeCase(external[caseName], caseName);
  }
}

function verifyImageEnvironment(value) {
  const environment = requireObject(value, "image.environment");
  for (const field of [
    "chromium",
    "cpu",
    "distribution",
    "fixtureFilesystem",
    "go",
    "installedMemoryKiB",
    "kernel",
    "logicalCPUs",
    "node",
    "npm",
    "playwright"
  ]) {
    requireEnvironmentValue(environment, field, "image.environment");
  }
  requireCondition(
    environment.go.includes("go1.26.5"),
    "image.environment.go",
    "expected Go 1.26.5"
  );
  requireEqual(environment.node, "v26.2.0", "image.environment.node");
  requireEqual(environment.npm, "11.13.0", "image.environment.npm");
  requireEqual(environment.playwright, "1.62.0", "image.environment.playwright");
}

function verifyObjectURLState(value, label, expected) {
  const counters = requireObject(value, label);
  for (const [field, expectedValue] of Object.entries(expected)) {
    requireEqual(counters[field], expectedValue, `${label}.${field}`);
  }
}

function verifyImageSample(value, label, applyThresholds) {
  const sample = requireObject(value, label);
  const elapsed = requireNumber(sample.elapsedMs, `${label}.elapsedMs`);
  if (applyThresholds) {
    requireMaximum(elapsed, 2000, `${label}.elapsedMs`);
  }
  requireMaximum(
    requireInteger(sample.aggregateMaximumActiveFetches, `${label}.aggregateMaximumActiveFetches`),
    8,
    `${label}.aggregateMaximumActiveFetches`
  );
  requireEqual(sample.successfulAssetResponses, 10, `${label}.successfulAssetResponses`);
  const transferredBytes = requireInteger(
    sample.successfulAssetBytes,
    `${label}.successfulAssetBytes`
  );
  requireCondition(
    transferredBytes >= exactImageBytes &&
      transferredBytes <= exactImageBytes + 10 * maximumTransferFramingBytes,
    `${label}.successfulAssetBytes`,
    "transfer bytes are outside the payload/framing bound"
  );
  const tabs = requireArray(sample.tabs, `${label}.tabs`, 2);
  for (const [index, tabValue] of tabs.entries()) {
    const tabLabel = `${label}.tabs[${String(index)}]`;
    const tab = requireObject(tabValue, tabLabel);
    requireEqual(tab.tab, `tab-${String(index + 1)}`, `${tabLabel}.tab`);
    requireMaximum(
      requireInteger(tab.maximumActiveFetches, `${tabLabel}.maximumActiveFetches`),
      4,
      `${tabLabel}.maximumActiveFetches`
    );
    requireEqual(tab.successfulAssetResponses, 5, `${tabLabel}.successfulAssetResponses`);
    const loaded = requireObject(tab.loaded, `${tabLabel}.loaded`);
    const created = requireArray(loaded.created, `${tabLabel}.loaded.created`, 5);
    const revoked = requireArray(loaded.revoked, `${tabLabel}.loaded.revoked`, 1);
    requireEqual(revoked[0], created[0], `${tabLabel}.loaded.lru`);
    verifyObjectURLState(loaded.counters, `${tabLabel}.loaded.counters`, {
      createdBytes: 5 * imageBytes,
      createdCount: 5,
      currentBytes: maximumRetainedBytes,
      currentCount: 4,
      maximumBytes: maximumRetainedBytes,
      maximumCount: 4,
      revokedBytes: imageBytes,
      revokedCount: 1
    });
    verifyObjectURLState(tab.cleanup, `${tabLabel}.cleanup`, {
      createdBytes: 5 * imageBytes,
      createdCount: 5,
      currentBytes: 0,
      currentCount: 0,
      maximumBytes: maximumRetainedBytes,
      maximumCount: 4,
      revokedBytes: 5 * imageBytes,
      revokedCount: 5
    });
  }
  const serverCounters = requireCounterObservation(
    sample.serverCounters,
    { assetStreamBytes: exactImageBytes },
    `${label}.serverCounters`
  );
  requireEqual(
    serverCounters.after.activeAssetStreams,
    0,
    `${label}.serverCounters.after.activeAssetStreams`
  );
  requireMaximum(
    serverCounters.after.maximumActiveAssetStreams,
    8,
    `${label}.serverCounters.after.maximumActiveAssetStreams`
  );
}

function verifyCompiledImageRejections(value) {
  const rejections = requireObject(value, "image.compiledHTTPRejections");
  const cases = requireArray(rejections.cases, "image.compiledHTTPRejections.cases", 3);
  const expected = [
    ["unsupported", 415, "assetUnsupportedType"],
    ["oversized", 413, "assetTooLarge"],
    ["traversal", 404, "assetNotFound"]
  ];
  for (const [index, expectedCase] of expected.entries()) {
    const label = `image.compiledHTTPRejections.cases[${String(index)}]`;
    const testCase = requireObject(cases[index], label);
    requireEqual(testCase.name, expectedCase[0], `${label}.name`);
    requireEqual(testCase.status, expectedCase[1], `${label}.status`);
    requireEqual(testCase.code, expectedCase[2], `${label}.code`);
  }
  const counters = requireCounterObservation(
    rejections.counters,
    { assetStreamBytes: 72 },
    "image.compiledHTTPRejections.counters"
  );
  requireEqual(
    counters.after.activeAssetStreams,
    0,
    "image.compiledHTTPRejections.counters.after.activeAssetStreams"
  );
  requireMaximum(
    counters.after.maximumActiveAssetStreams,
    8,
    "image.compiledHTTPRejections.counters.after.maximumActiveAssetStreams"
  );
}

export function verifyImageReport(reportValue) {
  const report = requireObject(reportValue, "image");
  requireEqual(report.generatorVersion, "gate-e-images-v1", "image.generatorVersion");
  verifyImageEnvironment(report.environment);
  requireArtifactIdentity(
    requireObject(report.artifactIdentity, "image.artifactIdentity").binary,
    "image.artifactIdentity.binary"
  );
  const policy = requireObject(report.samplePolicy, "image.samplePolicy");
  requireEqual(policy.reportedSamples, reportedSamples, "image.samplePolicy.reportedSamples");
  requireEqual(policy.warmupSamples, 1, "image.samplePolicy.warmupSamples");
  const fixture = requireObject(report.fixture, "image.fixture");
  requireEqual(
    fixture.sha256,
    "849366fdf43206de3d04b9c8b083dd6412a16903a8540c57e0aa23709a72ded6",
    "image.fixture.sha256"
  );
  const details = requireObject(fixture.details, "image.fixture.details");
  requireEqual(
    details.generatorVersion,
    "gate-e-images-v1",
    "image.fixture.details.generatorVersion"
  );
  requireEqual(details.imageCount, 5, "image.fixture.details.imageCount");
  requireEqual(details.imageSizeBytes, imageBytes, "image.fixture.details.imageSizeBytes");
  verifyCompiledImageRejections(report.compiledHTTPRejections);
  verifyImageSample(report.warmup, "image.warmup", false);
  const measured = requireObject(report.measured, "image.measured");
  const samples = requireArray(measured.samples, "image.measured.samples", reportedSamples);
  for (const [index, sample] of samples.entries()) {
    verifyImageSample(sample, `image.measured.samples[${String(index)}]`, true);
  }
  requireSummary(measured.elapsedMs, samples, "elapsedMs", "image.measured.elapsedMs");
  requireSummary(
    measured.aggregateMaximumActiveFetches,
    samples,
    "aggregateMaximumActiveFetches",
    "image.measured.aggregateMaximumActiveFetches"
  );
  const perTabValues = samples.flatMap((sample) =>
    requireArray(requireObject(sample, "image sample").tabs, "image sample tabs", 2).map((tab) =>
      requireInteger(
        requireObject(tab, "image tab").maximumActiveFetches,
        "image tab maximumActiveFetches"
      )
    )
  );
  const retainedValues = samples.flatMap((sample) =>
    sample.tabs.map(
      (tab) => requireObject(tab.loaded.counters, "image loaded counters").maximumBytes
    )
  );
  const perTabSummary = requireObject(
    measured.perTabMaximumActiveFetches,
    "image.measured.perTabMaximumActiveFetches"
  );
  requireEqual(
    perTabSummary.median,
    median(perTabValues),
    "image.measured.perTabMaximumActiveFetches.median"
  );
  requireEqual(
    perTabSummary.worst,
    Math.max(...perTabValues),
    "image.measured.perTabMaximumActiveFetches.worst"
  );
  const retainedSummary = requireObject(
    measured.maximumRetainedBytesPerTab,
    "image.measured.maximumRetainedBytesPerTab"
  );
  requireEqual(
    retainedSummary.median,
    median(retainedValues),
    "image.measured.maximumRetainedBytesPerTab.median"
  );
  requireEqual(
    retainedSummary.worst,
    Math.max(...retainedValues),
    "image.measured.maximumRetainedBytesPerTab.worst"
  );
  requireMaximum(
    retainedSummary.worst,
    maximumRetainedBytes,
    "image.measured.maximumRetainedBytesPerTab.worst"
  );
}

export function verifyReleaseArtifactIdentity(backendValue, imageValue, expectedSha256) {
  const backend = requireObject(backendValue, "backend");
  const image = requireObject(imageValue, "image");
  const backendIdentity = requireArtifactIdentity(
    requireObject(backend.checksums, "backend.checksums").binary,
    "backend.checksums.binary"
  );
  const imageIdentity = requireArtifactIdentity(
    requireObject(image.artifactIdentity, "image.artifactIdentity").binary,
    "image.artifactIdentity.binary"
  );
  requireEqual(backendIdentity.sha256, imageIdentity.sha256, "Gate E report binary identity");
  requireEqual(backendIdentity.sizeBytes, imageIdentity.sizeBytes, "Gate E report binary size");
  requireEqual(backendIdentity.sha256, expectedSha256, "Gate E release-candidate SHA-256");
}

async function readJSON(path, label) {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    fail(label, `cannot read valid JSON from ${path}: ${String(error)}`);
  }
}

async function main() {
  const [backendPath, imagePath, releaseBinaryPath] = process.argv.slice(2);
  if (!backendPath || !imagePath || !releaseBinaryPath || process.argv.length !== 5) {
    fail(
      "arguments",
      "usage: verify-thresholds.mjs <backend-baseline.json> <image-baseline.json> <release-binary>"
    );
  }
  const [backend, image, releaseBinary] = await Promise.all([
    readJSON(backendPath, "backend report"),
    readJSON(imagePath, "image report"),
    readFile(releaseBinaryPath)
  ]);
  verifyBackendReport(backend);
  verifyImageReport(image);
  verifyReleaseArtifactIdentity(
    backend,
    image,
    createHash("sha256").update(releaseBinary).digest("hex")
  );
  process.stdout.write("Gate E frozen report thresholds passed.\n");
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  main().catch((error) => {
    process.stderr.write(
      `Gate E threshold verification failed: ${String(error.message ?? error)}\n`
    );
    process.exitCode = 1;
  });
}
