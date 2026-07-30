import { expect, test, type Page, type Route } from "@playwright/test";

const baselineSource = `# M5 baseline

The active document remains stable while repository changes are reconciled.
`;
const changedSource = `# M5 changed

The exact Markdown bytes have changed on disk.
`;
const otherSource = "# Other document\n";
const baselineDocumentRevision = "1".repeat(64);
const changedDocumentRevision = "2".repeat(64);
const baselineDocumentMetadata = "a".repeat(64);
const touchedDocumentMetadata = "b".repeat(64);
const changedDocumentMetadata = "c".repeat(64);
const baselineReviewMetadata = "d".repeat(64);
const changedReviewMetadata = "e".repeat(64);
const baselineReviewRevision = "3".repeat(64);
const changedReviewRevision = "4".repeat(64);

type Availability = "ready" | "tooLarge";

interface MockMessage {
  id: string;
  author: {
    type: "human" | "agent";
    name: string;
  };
  body: string;
  createdAt: string;
}

interface MockThread {
  id: string;
  anchor: {
    type: "document";
  };
  attachment: {
    state: "document";
  };
  status: "open" | "handled" | "resolved";
  messages: MockMessage[];
}

interface MockDocument {
  path: string;
  source: string;
  revision: string;
  metadataRevision: string;
  availability: Availability;
}

interface StateRequest {
  since: string | null;
  startedAt: number;
  servedRevision?: number;
  status?: number;
}

interface StateFailure {
  status: 401 | 500;
  code: "workspaceUnavailable";
  message: string;
}

function reviewThread(): MockThread {
  return {
    id: "thread_m5",
    anchor: { type: "document" },
    attachment: { state: "document" },
    status: "open",
    messages: [
      {
        id: "message_m5",
        author: {
          type: "human",
          name: "Reviewer"
        },
        body: "Original review message",
        createdAt: "2026-07-29T00:00:00Z"
      }
    ]
  };
}

async function fulfillJSON(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

class MockM5Server {
  workspaceRevision = 1;
  readme: MockDocument | null = {
    path: "README.md",
    source: baselineSource,
    revision: baselineDocumentRevision,
    metadataRevision: baselineDocumentMetadata,
    availability: "ready"
  };
  other: MockDocument = {
    path: "OTHER.md",
    source: otherSource,
    revision: "9".repeat(64),
    metadataRevision: "9".repeat(64),
    availability: "ready"
  };
  reviewDocumentRevision = baselineDocumentRevision;
  reviewRevision = baselineReviewRevision;
  reviewMetadataRevision: string | null = baselineReviewMetadata;
  threads: MockThread[] = [reviewThread()];
  stateRequests: StateRequest[] = [];
  documentGets = 0;
  reviewGets = 0;
  activeStateRequests = 0;
  maximumActiveStateRequests = 0;
  mutationBodies: unknown[] = [];
  nextStateFailure: StateFailure | null = null;
  #nextConditionalGate: Promise<void> | null = null;

  holdNextConditionalRequest(): () => void {
    let release: (() => void) | undefined;
    this.#nextConditionalGate = new Promise<void>((resolve) => {
      release = resolve;
    });
    return () => {
      release?.();
    };
  }

  advanceWorkspace(): void {
    this.workspaceRevision += 1;
  }

  strictChangedState(): unknown {
    const navigation: unknown[] = [
      {
        kind: "document",
        name: this.other.path,
        path: this.other.path,
        sizeBytes: new TextEncoder().encode(this.other.source).length,
        availability: this.other.availability,
        documentMetadataRevision: this.other.metadataRevision,
        reviewMetadataRevision: null
      }
    ];
    if (this.readme) {
      navigation.push({
        kind: "document",
        name: this.readme.path,
        path: this.readme.path,
        sizeBytes: new TextEncoder().encode(this.readme.source).length,
        availability: this.readme.availability,
        documentMetadataRevision: this.readme.metadataRevision,
        reviewMetadataRevision: this.reviewMetadataRevision
      });
    }
    return {
      status: "changed",
      workspaceRevision: this.workspaceRevision,
      documentCount: navigation.length,
      initialDocumentPath: this.readme ? this.readme.path : this.other.path,
      navigation,
      warnings: []
    };
  }

  async install(page: Page): Promise<void> {
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const url = new URL(request.url());

      if (url.pathname === "/api/state") {
        await this.#handleState(route, url);
        return;
      }
      if (url.pathname === "/api/document") {
        this.documentGets += 1;
        const path = url.searchParams.get("path");
        const document =
          path === "README.md" ? this.readme : path === "OTHER.md" ? this.other : null;
        if (!document || document.availability !== "ready") {
          await fulfillJSON(route, 404, {
            error: {
              code: "documentNotFound",
              message: "This Markdown document was not found."
            }
          });
          return;
        }
        await fulfillJSON(route, 200, {
          path: document.path,
          revision: document.revision,
          source: document.source
        });
        return;
      }
      if (url.pathname === "/api/review") {
        this.reviewGets += 1;
        await fulfillJSON(route, 200, {
          path: "README.md",
          documentRevision: this.reviewDocumentRevision,
          reviewRevision: this.reviewRevision,
          threads: this.threads
        });
        return;
      }
      if (
        (request.method() === "POST" && /^\/api\/threads\/[^/]+\/messages$/u.test(url.pathname)) ||
        (request.method() === "PATCH" && /^\/api\/messages\/[^/]+$/u.test(url.pathname))
      ) {
        this.mutationBodies.push(request.postDataJSON());
        await fulfillJSON(route, 409, {
          error: {
            code: "reviewChanged",
            message: "The review changed."
          },
          current: {
            documentRevision: this.reviewDocumentRevision,
            reviewRevision: this.reviewRevision
          }
        });
        return;
      }

      await fulfillJSON(route, 404, {
        error: {
          code: "endpointNotFound",
          message: "This API endpoint does not exist."
        }
      });
    });
  }

  async #handleState(route: Route, url: URL): Promise<void> {
    const record: StateRequest = {
      since: url.searchParams.get("since"),
      startedAt: Date.now()
    };
    this.stateRequests.push(record);
    this.activeStateRequests += 1;
    this.maximumActiveStateRequests = Math.max(
      this.maximumActiveStateRequests,
      this.activeStateRequests
    );
    try {
      if (record.since !== null && this.#nextConditionalGate) {
        const gate = this.#nextConditionalGate;
        this.#nextConditionalGate = null;
        await gate;
      }
      if (record.since !== null && this.nextStateFailure) {
        const failure = this.nextStateFailure;
        this.nextStateFailure = null;
        record.status = failure.status;
        await fulfillJSON(route, failure.status, {
          error: {
            code: failure.code,
            message: failure.message
          }
        });
        return;
      }
      record.status = 200;
      record.servedRevision = this.workspaceRevision;
      if (record.since === String(this.workspaceRevision)) {
        await fulfillJSON(route, 200, {
          status: "unchanged",
          workspaceRevision: this.workspaceRevision
        });
        return;
      }
      await fulfillJSON(route, 200, this.strictChangedState());
    } finally {
      this.activeStateRequests -= 1;
    }
  }
}

async function installVisibilityControl(page: Page): Promise<void> {
  await page.addInitScript(() => {
    let visibility: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility
    });
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => visibility !== "visible"
    });
    Object.defineProperty(window, "__m5SetVisibility", {
      configurable: true,
      value: (next: DocumentVisibilityState) => {
        visibility = next;
        document.dispatchEvent(new Event("visibilitychange"));
      }
    });
  });
}

async function setVisibility(page: Page, state: DocumentVisibilityState): Promise<void> {
  await page.evaluate((next) => {
    const controlledWindow = window as typeof window & {
      __m5SetVisibility: (visibility: DocumentVisibilityState) => void;
    };
    controlledWindow.__m5SetVisibility(next);
  }, state);
}

async function openWorkspace(
  page: Page,
  server: MockM5Server
): Promise<{
  documentGets: number;
  reviewGets: number;
  stateRequests: number;
}> {
  await server.install(page);
  await page.goto(`/`);
  await expect(page.getByRole("heading", { level: 1, name: "M5 baseline" })).toBeVisible();
  await expect.poll(() => server.documentGets).toBe(1);
  await expect.poll(() => server.reviewGets).toBe(1);
  return {
    documentGets: server.documentGets,
    reviewGets: server.reviewGets,
    stateRequests: server.stateRequests.length
  };
}

async function installMarkdownMutationCounter(page: Page): Promise<void> {
  await page.evaluate(() => {
    const documentContent = document.querySelector(".document-content");
    if (!documentContent) {
      throw new Error("document content is missing");
    }
    const countedWindow = window as typeof window & {
      __m5MarkdownMutations?: number;
    };
    countedWindow.__m5MarkdownMutations = 0;
    const observer = new MutationObserver((records) => {
      countedWindow.__m5MarkdownMutations =
        (countedWindow.__m5MarkdownMutations ?? 0) + records.length;
    });
    observer.observe(documentContent, {
      childList: true,
      characterData: true,
      subtree: true
    });
  });
}

async function resetMarkdownMutationCount(page: Page): Promise<void> {
  await page.evaluate(() => {
    (window as typeof window & { __m5MarkdownMutations?: number }).__m5MarkdownMutations = 0;
  });
}

async function markdownMutationCount(page: Page): Promise<number> {
  return page.evaluate(
    () => (window as typeof window & { __m5MarkdownMutations?: number }).__m5MarkdownMutations ?? 0
  );
}

test("uses conditional non-overlapping cadence, pauses while hidden, and resumes immediately", async ({
  page
}) => {
  await installVisibilityControl(page);
  const server = new MockM5Server();
  const baseline = await openWorkspace(page, server);
  expect(baseline).toEqual({
    documentGets: 1,
    reviewGets: 1,
    stateRequests: 1
  });
  const releaseConditional = server.holdNextConditionalRequest();

  await expect
    .poll(() => server.stateRequests.length, { timeout: 3_000 })
    .toBe(baseline.stateRequests + 1);
  expect(server.stateRequests.at(-1)?.since).toBe("1");
  await page.waitForTimeout(1_100);
  expect(server.stateRequests).toHaveLength(2);
  expect(server.maximumActiveStateRequests).toBe(1);

  releaseConditional();
  await expect.poll(() => server.activeStateRequests).toBe(0);
  await expect
    .poll(() => server.stateRequests.length, { timeout: 2_000 })
    .toBeGreaterThanOrEqual(3);
  expect(server.maximumActiveStateRequests).toBe(1);
  const conditionalStarts = server.stateRequests
    .filter(({ since }) => since !== null)
    .map(({ startedAt }) => startedAt);
  expect((conditionalStarts[1] ?? 0) - (conditionalStarts[0] ?? 0)).toBeGreaterThanOrEqual(1_000);

  await expect.poll(() => server.activeStateRequests).toBe(0);
  await setVisibility(page, "hidden");
  const hiddenCount = server.stateRequests.length;
  await page.waitForTimeout(1_200);
  expect(server.stateRequests).toHaveLength(hiddenCount);

  const resumedAt = Date.now();
  await setVisibility(page, "visible");
  await expect.poll(() => server.stateRequests.length, { timeout: 750 }).toBe(hiddenCount + 1);
  const resumedRequest = server.stateRequests.at(-1);
  expect(resumedRequest?.since).toBe("1");
  expect((resumedRequest?.startedAt ?? Number.POSITIVE_INFINITY) - resumedAt).toBeLessThan(750);
});

test("selectively reconciles unrelated, sidecar-only, metadata-only, and exact document changes", async ({
  page
}) => {
  const server = new MockM5Server();
  const baseline = await openWorkspace(page, server);
  await installMarkdownMutationCounter(page);

  server.other.metadataRevision = "f".repeat(64);
  server.advanceWorkspace();
  await expect
    .poll(() =>
      server.stateRequests.some(
        ({ since, servedRevision }) => since === "1" && servedRevision === 2
      )
    )
    .toBe(true);
  await page.waitForTimeout(50);
  expect(server.documentGets).toBe(baseline.documentGets);
  expect(server.reviewGets).toBe(baseline.reviewGets);

  await resetMarkdownMutationCount(page);
  server.threads = [
    reviewThread(),
    {
      id: "thread_external",
      anchor: { type: "document" },
      attachment: { state: "document" },
      status: "handled",
      messages: [
        {
          id: "message_external",
          author: { type: "agent", name: "Codex" },
          body: "External sidecar-only reply",
          createdAt: "2026-07-29T00:01:00Z"
        }
      ]
    }
  ];
  server.reviewRevision = changedReviewRevision;
  server.reviewMetadataRevision = changedReviewMetadata;
  server.advanceWorkspace();
  await expect(page.getByText("External sidecar-only reply")).toBeVisible();
  expect(server.documentGets).toBe(baseline.documentGets);
  expect(server.reviewGets).toBe(baseline.reviewGets + 1);
  expect(await markdownMutationCount(page)).toBe(0);

  await resetMarkdownMutationCount(page);
  if (!server.readme) {
    throw new Error("README fixture is missing");
  }
  server.readme.metadataRevision = touchedDocumentMetadata;
  server.advanceWorkspace();
  await expect.poll(() => server.documentGets).toBe(baseline.documentGets + 1);
  await expect(page.getByRole("heading", { level: 1, name: "M5 baseline" })).toBeVisible();
  expect(server.reviewGets).toBe(baseline.reviewGets + 1);
  expect(await markdownMutationCount(page)).toBe(0);

  await resetMarkdownMutationCount(page);
  server.readme.source = changedSource;
  server.readme.revision = changedDocumentRevision;
  server.readme.metadataRevision = changedDocumentMetadata;
  server.reviewDocumentRevision = changedDocumentRevision;
  server.advanceWorkspace();
  await expect(page.getByRole("heading", { level: 1, name: "M5 changed" })).toBeVisible();
  expect(server.documentGets).toBe(baseline.documentGets + 2);
  expect(server.reviewGets).toBe(baseline.reviewGets + 2);
  expect(await markdownMutationCount(page)).toBeGreaterThan(0);
});

test("freezes an open new-comment draft until cancellation triggers fresh reconciliation", async ({
  page
}) => {
  const server = new MockM5Server();
  const baseline = await openWorkspace(page, server);

  await page.getByRole("button", { name: "Comment on document" }).click();
  const comment = page.getByRole("textbox", { name: "Comment" });
  await comment.fill("Keep this new comment draft");
  if (!server.readme) {
    throw new Error("README fixture is missing");
  }
  server.readme.source = changedSource;
  server.readme.revision = changedDocumentRevision;
  server.readme.metadataRevision = changedDocumentMetadata;
  server.reviewDocumentRevision = changedDocumentRevision;
  server.advanceWorkspace();

  await expect(
    page.getByText("Document changed on disk. Finish or discard your comment to reload.", {
      exact: true
    })
  ).toBeVisible();
  await expect(comment).toHaveValue("Keep this new comment draft");
  await expect(page.getByRole("heading", { level: 1, name: "M5 baseline" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "M5 changed" })).toHaveCount(0);
  expect(server.documentGets).toBe(baseline.documentGets + 1);
  expect(server.reviewGets).toBe(baseline.reviewGets);

  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "M5 changed" })).toBeVisible();
  expect(server.reviewGets).toBe(baseline.reviewGets + 1);
});

test("freezes an open reply draft and submits its captured revisions", async ({ page }) => {
  const server = new MockM5Server();
  const baseline = await openWorkspace(page, server);
  const card = page.locator('.thread-card[data-thread-id="thread_m5"]');
  const draft = "Reply draft survives external changes";

  await card.getByRole("button", { name: "Reply" }).click();
  await card.getByRole("textbox", { name: "Reply" }).fill(draft);

  server.reviewMetadataRevision = changedReviewMetadata;
  server.reviewRevision = changedReviewRevision;
  server.advanceWorkspace();
  await expect.poll(() => server.reviewGets).toBe(baseline.reviewGets + 1);

  const editor = page.getByRole("textbox", { name: "Reply" });
  await expect(editor).toHaveValue(draft);

  if (!server.readme) {
    throw new Error("README fixture is missing");
  }
  server.readme.source = changedSource;
  server.readme.revision = changedDocumentRevision;
  server.readme.metadataRevision = changedDocumentMetadata;
  server.reviewDocumentRevision = changedDocumentRevision;
  server.advanceWorkspace();
  await expect(
    page.getByText("Document changed on disk. Finish or discard your comment to reload.", {
      exact: true
    })
  ).toBeVisible();
  await expect(editor).toHaveValue(draft);
  await expect(page.getByRole("heading", { level: 1, name: "M5 baseline" })).toBeVisible();

  await editor.press("Control+Enter");
  await expect(editor).toHaveValue(draft);
  await expect(page.getByRole("alert").filter({ hasText: "draft has been kept" })).toBeVisible();
  expect(server.mutationBodies).toHaveLength(1);
  expect(server.mutationBodies[0]).toMatchObject({
    expectedDocumentRevision: baselineDocumentRevision,
    expectedReviewRevision: baselineReviewRevision
  });
});

for (const transition of ["removed", "oversized"] as const) {
  test(`shows the contextual ${transition} state for an active document without a draft`, async ({
    page
  }) => {
    const server = new MockM5Server();
    await openWorkspace(page, server);
    const documentGetsBefore = server.documentGets;
    const reviewGetsBefore = server.reviewGets;

    if (transition === "removed") {
      server.readme = null;
    } else {
      if (!server.readme) {
        throw new Error("README fixture is missing");
      }
      server.readme.availability = "tooLarge";
      server.readme.metadataRevision = changedDocumentMetadata;
    }
    server.advanceWorkspace();

    await expect(
      page.getByRole("heading", {
        name:
          transition === "removed"
            ? "This document is no longer available"
            : "This document is too large"
      })
    ).toBeVisible();
    expect(server.documentGets).toBe(documentGetsBefore);
    expect(server.reviewGets).toBe(reviewGetsBefore);
  });
}

test("retains stable content after a transient state failure and resumes polling", async ({
  page
}) => {
  const server = new MockM5Server();
  await openWorkspace(page, server);

  server.nextStateFailure = {
    status: 500,
    code: "workspaceUnavailable",
    message: "The workspace could not be scanned."
  };
  await expect(
    page.getByText("Workspace changes could not be checked. mdReview will try again.", {
      exact: true
    })
  ).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "M5 baseline" })).toBeVisible();
  await expect(
    page.getByText("Workspace changes could not be checked. mdReview will try again.", {
      exact: true
    })
  ).toHaveCount(0, { timeout: 3_000 });
  const resumedRequestCount = server.stateRequests.length;
  await page.waitForTimeout(1_200);
  expect(server.stateRequests.length).toBeGreaterThan(resumedRequestCount);
});
