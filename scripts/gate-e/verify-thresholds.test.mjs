import assert from "node:assert/strict";
import test from "node:test";

import {
  verifyBackendReport,
  verifyImageReport,
  verifyReleaseArtifactIdentity
} from "./verify-thresholds.mjs";

const imageBytes = 10 * 1024 * 1024;
const retainedBytes = 40 * 1024 * 1024;
const exactImageBytes = 100 * 1024 * 1024;
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

function counters(overrides = {}) {
  return Object.fromEntries(counterFields.map((field) => [field, overrides[field] ?? 0]));
}

function counterObservation(deltaOverrides = {}) {
  const before = counters();
  const delta = counters(deltaOverrides);
  const after = counters(deltaOverrides);
  return { after, before, delta };
}

function summary(samples, field) {
  const values = samples.map((sample) => sample[field]);
  const sorted = [...values].sort((left, right) => left - right);
  return {
    median: sorted[Math.floor(sorted.length / 2)],
    worst: Math.max(...values)
  };
}

function latencyMeasurement(elapsedMs, counterDelta, extra = {}) {
  const makeSample = () => ({
    counters: counterObservation(counterDelta),
    elapsedMs,
    rssKiB: 12_000,
    ...extra
  });
  const samples = Array.from({ length: 5 }, makeSample);
  return {
    summary: {
      elapsedMs: summary(samples, "elapsedMs"),
      rssKiB: summary(samples, "rssKiB"),
      samples
    },
    warmup: makeSample()
  };
}

function coldMeasurement(fixtureName) {
  const ignore = fixtureName === "ignored-tree";
  const makeSample = () => ({
    counters: counters({
      completeWorkspaceScans: 1,
      gitignoreContentBytes: ignore ? 9 : 0,
      gitignoreContentOpens: ignore ? 1 : 0
    }),
    elapsedMs: 5,
    rssKiB: 12_000
  });
  const samples = Array.from({ length: 5 }, makeSample);
  return {
    summary: {
      elapsedMs: summary(samples, "elapsedMs"),
      rssKiB: summary(samples, "rssKiB"),
      samples
    },
    warmup: makeSample()
  };
}

function idleMeasurement() {
  const makeSample = () => ({
    counters: counterObservation(),
    cpuTicksDelta: 0,
    durationMs: 1000,
    rssDeltaKiB: 0,
    rssEndKiB: 12_000,
    rssStartKiB: 12_000
  });
  const samples = Array.from({ length: 5 }, makeSample);
  return {
    summary: {
      cpuTicksDelta: summary(samples, "cpuTicksDelta"),
      rssAbsoluteDeltaKiB: {
        median: 0,
        worst: 0
      },
      samples
    },
    warmup: makeSample()
  };
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

function externalExpectedBytes(caseName, index) {
  if (caseName === "externalDocumentChange") {
    const sequence = index + 2;
    return {
      document: Buffer.byteLength(`# External document change\n\nSequence ${String(sequence)}\n`),
      sidecar: Buffer.byteLength(emptySidecar())
    };
  }
  if (caseName === "externalSidecarChange") {
    const sequence = index + 8;
    return {
      document: Buffer.byteLength("# External document change\n\nSequence 6\n"),
      sidecar: Buffer.byteLength(
        sidecarWithMessage(`External sidecar sequence ${String(sequence)}`, sequence)
      )
    };
  }
  const sequence = index + 14;
  return {
    document: Buffer.byteLength(`# Simultaneous external change\n\nSequence ${String(sequence)}\n`),
    sidecar: Buffer.byteLength(
      sidecarWithMessage(`Simultaneous sidecar sequence ${String(sequence)}`, sequence)
    )
  };
}

function externalMeasurement(caseName) {
  const makeSample = (index) => {
    const bytes = externalExpectedBytes(caseName, index);
    return {
      counters: counterObservation({
        completeWorkspaceScans: 1,
        markdownContentBytes: bytes.document * 3,
        markdownContentOpens: 3,
        sidecarContentBytes: bytes.sidecar,
        sidecarContentOpens: 1
      }),
      rssKiB: 12_000,
      stateRequestMs: 5,
      visibleAfterMutationMs: 1100
    };
  };
  const samples = Array.from({ length: 5 }, (_, index) => makeSample(index));
  return {
    summary: {
      rssKiB: summary(samples, "rssKiB"),
      samples,
      stateRequestMs: summary(samples, "stateRequestMs"),
      visibleAfterMutationMs: summary(samples, "visibleAfterMutationMs")
    },
    warmup: makeSample(0)
  };
}

function backendFixture(name, visibleDocumentCount, ignoredDocumentCount, fileCount, sha256) {
  return {
    fileCount,
    ignoredDocumentCount,
    manifest: { path: `manifests/${name}.json`, sha256 },
    name,
    visibleDocumentCount
  };
}

function passingBackendReport() {
  const fixtures = [
    backendFixture(
      "workspace-10",
      10,
      0,
      20,
      "4d59e6efee8c558c4154e38779eb7ffb02d1ff0d1b64624f6e5f25a1a35afd68"
    ),
    backendFixture(
      "workspace-100",
      100,
      0,
      200,
      "ccae72c8ec61045021c03bdf2bf66a7f862d2b3fa2c2b6aeb89fece371bf43ce"
    ),
    backendFixture(
      "workspace-1000",
      1000,
      0,
      2000,
      "6283a1bcebdaf9cb46927f5d81d802836bd6117062bfd05e133a3e9f242edbc4"
    ),
    backendFixture(
      "ignored-tree",
      100,
      5000,
      5201,
      "225b6daf97e6ca0bc4c7248f1657f3b51ee345ec44078fccbc0cb4fcb5e51a21"
    ),
    backendFixture(
      "image-heavy",
      1,
      0,
      5,
      "9b56b484e48aa2e0475d72cdc7afe69d9fadf8cbc28159bce650cf3804e7fd23"
    ),
    backendFixture(
      "external-change",
      1,
      0,
      2,
      "e67805d0db169a6a36b831235ce0b4121b5a37e0cdd1703af957427c9d09d363"
    )
  ];
  const measurements = {};
  for (const fixtureName of ["workspace-10", "workspace-100", "workspace-1000", "ignored-tree"]) {
    measurements[fixtureName] = {
      coldProcessReady: coldMeasurement(fixtureName),
      concurrentStaleConditional: latencyMeasurement(
        5,
        { completeWorkspaceScans: 1 },
        { requestCount: 5 }
      ),
      idleNoRequests: idleMeasurement(),
      unchangedConditional: latencyMeasurement(1, {}),
      warmFreshFullState: latencyMeasurement(2, {}),
      warmStaleFullState: latencyMeasurement(5, { completeWorkspaceScans: 1 })
    };
  }
  measurements["external-change"] = {
    externalDocumentChange: externalMeasurement("externalDocumentChange"),
    externalSidecarChange: externalMeasurement("externalSidecarChange"),
    simultaneousDocumentAndSidecarChange: externalMeasurement(
      "simultaneousDocumentAndSidecarChange"
    )
  };
  return {
    checksums: {
      binary: {
        sha256: "1".repeat(64),
        sizeBytes: 12_345
      },
      fixtureManifest: {
        sha256: "aaf2b64bcc221c96f5c4adac680996a46bbd21f73d4bafbff093a1cafcca3e1e"
      }
    },
    environment: {
      architecture: "x86_64",
      browsers: {
        chromium: "Chromium 151",
        firefox: "Firefox 153",
        playwright: "Version 1.62.0"
      },
      cpu: { logicalCount: 12, model: "test CPU" },
      distribution: "Test Linux",
      filesystem: { classification: "local", type: "tmpfs" },
      go: "go version go1.26.5 linux/amd64",
      installedMemoryKiB: 1_000_000,
      kernel: "Linux test",
      node: "v26.2.0",
      npm: "11.13.0"
    },
    fixtureGeneratorVersion: "gate-e-backend-v1",
    fixtures,
    measurementLimits: { pendingM7: [] },
    measurements,
    reportedSamplesPerCase: 5,
    schemaVersion: 1
  };
}

function objectURLCounters(cleaned) {
  return {
    createdBytes: 5 * imageBytes,
    createdCount: 5,
    currentBytes: cleaned ? 0 : retainedBytes,
    currentCount: cleaned ? 0 : 4,
    maximumBytes: retainedBytes,
    maximumCount: 4,
    revokedBytes: cleaned ? 5 * imageBytes : imageBytes,
    revokedCount: cleaned ? 5 : 1
  };
}

function imageSample() {
  const tabs = [0, 1].map((index) => {
    const created = Array.from({ length: 5 }, (_, imageIndex) => {
      return `blob:tab-${String(index + 1)}-${String(imageIndex + 1)}`;
    });
    return {
      cleanup: objectURLCounters(true),
      loaded: {
        counters: objectURLCounters(false),
        created,
        revoked: [created[0]]
      },
      maximumActiveFetches: 4,
      successfulAssetBytes: exactImageBytes / 2,
      successfulAssetResponses: 5,
      tab: `tab-${String(index + 1)}`
    };
  });
  return {
    aggregateMaximumActiveFetches: 8,
    elapsedMs: 200,
    sampleIndex: 1,
    serverCounters: counterObservation({
      assetStreamBytes: exactImageBytes,
      maximumActiveAssetStreams: 8
    }),
    successfulAssetBytes: exactImageBytes,
    successfulAssetResponses: 10,
    tabs
  };
}

function passingImageReport() {
  const samples = Array.from({ length: 5 }, imageSample);
  return {
    artifactIdentity: {
      binary: {
        sha256: "1".repeat(64),
        sizeBytes: 12_345
      }
    },
    compiledHTTPRejections: {
      cases: [
        { code: "assetUnsupportedType", name: "unsupported", status: 415 },
        { code: "assetTooLarge", name: "oversized", status: 413 },
        { code: "assetNotFound", name: "traversal", status: 404 }
      ],
      counters: counterObservation({
        assetStreamBytes: 72,
        maximumActiveAssetStreams: 1
      })
    },
    environment: {
      chromium: "Chromium 151",
      cpu: "test CPU",
      distribution: "Test Linux",
      fixtureFilesystem: "tmpfs",
      go: "go version go1.26.5 linux/amd64",
      installedMemoryKiB: "1000000",
      kernel: "Linux test",
      logicalCPUs: "12",
      node: "v26.2.0",
      npm: "11.13.0",
      playwright: "1.62.0"
    },
    fixture: {
      details: {
        generatorVersion: "gate-e-images-v1",
        imageCount: 5,
        imageSizeBytes: imageBytes
      },
      sha256: "849366fdf43206de3d04b9c8b083dd6412a16903a8540c57e0aa23709a72ded6"
    },
    generatorVersion: "gate-e-images-v1",
    measured: {
      aggregateMaximumActiveFetches: summary(samples, "aggregateMaximumActiveFetches"),
      elapsedMs: summary(samples, "elapsedMs"),
      maximumRetainedBytesPerTab: { median: retainedBytes, worst: retainedBytes },
      perTabMaximumActiveFetches: { median: 4, worst: 4 },
      samples
    },
    samplePolicy: { reportedSamples: 5, warmupSamples: 1 },
    warmup: imageSample()
  };
}

function setBackendLatency(report, caseName, value) {
  const measurement = report.measurements["workspace-10"][caseName];
  for (const sample of measurement.summary.samples) {
    sample.elapsedMs = value;
  }
  measurement.summary.elapsedMs = { median: value, worst: value };
}

function setImageElapsed(report, value) {
  for (const sample of report.measured.samples) {
    sample.elapsedMs = value;
  }
  report.measured.elapsedMs = { median: value, worst: value };
}

test("accepts complete reports at the frozen Gate E boundary", () => {
  const backend = passingBackendReport();
  const image = passingImageReport();
  assert.doesNotThrow(() => verifyBackendReport(backend));
  assert.doesNotThrow(() => verifyImageReport(image));
  assert.doesNotThrow(() => verifyReleaseArtifactIdentity(backend, image, "1".repeat(64)));
});

test("rejects reports measured against a different release binary", () => {
  const backend = passingBackendReport();
  const image = passingImageReport();
  image.artifactIdentity.binary.sha256 = "2".repeat(64);
  assert.throws(() => verifyReleaseArtifactIdentity(backend, image, "1".repeat(64)));
});

test("rejects each frozen backend threshold or counter mismatch", async (context) => {
  const cases = [
    ["reported samples", (report) => (report.reportedSamplesPerCase = 4)],
    ["fixture identity", (report) => (report.fixtures[0].visibleDocumentCount = 11)],
    ["cold latency", (report) => setBackendLatency(report, "coldProcessReady", 251)],
    ["stale latency", (report) => setBackendLatency(report, "warmStaleFullState", 101)],
    ["fresh latency", (report) => setBackendLatency(report, "warmFreshFullState", 51)],
    ["unchanged latency", (report) => setBackendLatency(report, "unchangedConditional", 21)],
    [
      "concurrent latency",
      (report) => setBackendLatency(report, "concurrentStaleConditional", 151)
    ],
    [
      "external state latency",
      (report) => {
        const measurement = report.measurements["external-change"].externalDocumentChange.summary;
        for (const sample of measurement.samples) sample.stateRequestMs = 101;
        measurement.stateRequestMs = { median: 101, worst: 101 };
      }
    ],
    [
      "external visibility",
      (report) => {
        const measurement = report.measurements["external-change"].externalDocumentChange.summary;
        for (const sample of measurement.samples) sample.visibleAfterMutationMs = 1501;
        measurement.visibleAfterMutationMs = { median: 1501, worst: 1501 };
      }
    ],
    [
      "RSS ceiling",
      (report) => {
        const measurement = report.measurements["workspace-10"].warmFreshFullState.summary;
        for (const sample of measurement.samples) sample.rssKiB = 65_537;
        measurement.rssKiB = { median: 65_537, worst: 65_537 };
      }
    ],
    [
      "idle CPU stability",
      (report) => {
        const measurement = report.measurements["workspace-10"].idleNoRequests.summary;
        for (const sample of measurement.samples) sample.cpuTicksDelta = 2;
        measurement.cpuTicksDelta = { median: 2, worst: 2 };
      }
    ],
    [
      "idle RSS stability",
      (report) => {
        const measurement = report.measurements["workspace-10"].idleNoRequests.summary;
        for (const sample of measurement.samples) {
          sample.rssDeltaKiB = 257;
          sample.rssEndKiB = sample.rssStartKiB + 257;
        }
        measurement.rssAbsoluteDeltaKiB = { median: 257, worst: 257 };
      }
    ],
    [
      "cold scan count",
      (report) => {
        report.measurements[
          "workspace-10"
        ].coldProcessReady.summary.samples[0].counters.completeWorkspaceScans = 2;
      }
    ],
    [
      "stale scan count",
      (report) => {
        const observation =
          report.measurements["workspace-10"].warmStaleFullState.summary.samples[0].counters;
        observation.after.completeWorkspaceScans = 2;
        observation.delta.completeWorkspaceScans = 2;
      }
    ],
    [
      "fresh scan count",
      (report) => {
        const observation =
          report.measurements["workspace-10"].warmFreshFullState.summary.samples[0].counters;
        observation.after.completeWorkspaceScans = 1;
        observation.delta.completeWorkspaceScans = 1;
      }
    ],
    [
      "metadata content open",
      (report) => {
        const observation =
          report.measurements["workspace-10"].unchangedConditional.summary.samples[0].counters;
        observation.after.markdownContentOpens = 1;
        observation.delta.markdownContentOpens = 1;
      }
    ],
    [
      "concurrent scan count",
      (report) => {
        const observation =
          report.measurements["workspace-10"].concurrentStaleConditional.summary.samples[0]
            .counters;
        observation.after.completeWorkspaceScans = 2;
        observation.delta.completeWorkspaceScans = 2;
      }
    ],
    [
      "ignore-file bytes",
      (report) => {
        report.measurements[
          "ignored-tree"
        ].coldProcessReady.summary.samples[0].counters.gitignoreContentBytes = 10;
      }
    ],
    [
      "external content bytes",
      (report) => {
        const observation =
          report.measurements["external-change"].externalDocumentChange.summary.samples[0].counters;
        observation.after.markdownContentBytes += 1;
        observation.delta.markdownContentBytes += 1;
      }
    ]
  ];

  for (const [name, mutate] of cases) {
    await context.test(name, () => {
      const report = passingBackendReport();
      mutate(report);
      assert.throws(() => verifyBackendReport(report));
    });
  }
});

test("rejects each frozen image threshold or lifecycle counter mismatch", async (context) => {
  const cases = [
    ["reported samples", (report) => (report.samplePolicy.reportedSamples = 4)],
    ["fixture identity", (report) => (report.fixture.details.imageCount = 6)],
    ["decode latency", (report) => setImageElapsed(report, 2001)],
    [
      "frontend concurrency",
      (report) => {
        report.measured.samples[0].tabs[0].maximumActiveFetches = 5;
      }
    ],
    [
      "server concurrency",
      (report) => {
        const observation = report.measured.samples[0].serverCounters;
        observation.after.maximumActiveAssetStreams = 9;
        observation.delta.maximumActiveAssetStreams = 9;
      }
    ],
    [
      "retained bytes",
      (report) => {
        report.measured.samples[0].tabs[0].loaded.counters.maximumBytes = retainedBytes + 1;
      }
    ],
    [
      "exact asset bytes",
      (report) => {
        const observation = report.measured.samples[0].serverCounters;
        observation.after.assetStreamBytes = exactImageBytes - 1;
        observation.delta.assetStreamBytes = exactImageBytes - 1;
      }
    ],
    [
      "navigation cleanup",
      (report) => {
        report.measured.samples[0].tabs[0].cleanup.currentBytes = 1;
      }
    ],
    [
      "active SVG byte count",
      (report) => {
        const observation = report.compiledHTTPRejections.counters;
        observation.after.assetStreamBytes = 73;
        observation.delta.assetStreamBytes = 73;
      }
    ]
  ];

  for (const [name, mutate] of cases) {
    await context.test(name, () => {
      const report = passingImageReport();
      mutate(report);
      assert.throws(() => verifyImageReport(report));
    });
  }
});
