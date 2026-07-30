import { expect, test, type Page, type Route } from "@playwright/test";

const documentRevision = "d0820a0afd1e1aa6b8bbf91c8f6915e6d544eec8be1c032f7779a5e6a6b7b908";
const reviewRevision = "c".repeat(64);
const changedDocumentRevision = "e".repeat(64);
const source = `# Review target

Alpha beta gamma and overlap phrase here.

Second paragraph for keyboard selection.

Entity: &#x1F642;.
`;

interface RangeOffsets {
  start: number;
  end: number;
}

interface MockServer {
  documentRevision: string;
  reviewDocumentRevision?: string;
  reviewRevision: string | null;
  threads: unknown[];
  documentGets: number;
  reviewGets: number;
  posts: Array<{
    headers: Record<string, string>;
    body: unknown;
  }>;
  create?: (route: Route, body: Record<string, unknown>) => Promise<void>;
}

function byteRange(needle: string): RangeOffsets {
  const sourceStart = source.indexOf(needle);
  if (sourceStart < 0) {
    throw new Error(`Missing ${needle}`);
  }
  const encoder = new TextEncoder();
  return {
    start: encoder.encode(source.slice(0, sourceStart)).length,
    end: encoder.encode(source.slice(0, sourceStart + needle.length)).length
  };
}

function textThread(
  id: string,
  text: string,
  message: string,
  attachment: "attached" | "detached" = "attached"
): unknown {
  const range = byteRange(text);
  return {
    id,
    anchor: {
      type: "text",
      range,
      source: text,
      text
    },
    attachment:
      attachment === "attached"
        ? { state: "attached", currentRange: range }
        : { state: "detached" },
    status: "open",
    messages: [
      {
        id: `message_${id}`,
        author: {
          type: "agent",
          name: "Test agent"
        },
        body: message,
        createdAt: "2026-07-28T14:30:00Z"
      }
    ]
  };
}

function documentThread(id: string, body: string): unknown {
  return {
    id,
    anchor: { type: "document" },
    attachment: { state: "document" },
    status: "handled",
    messages: [
      {
        id: `message_${id}`,
        author: {
          type: "human",
          name: "Reviewer"
        },
        body,
        createdAt: "2026-07-28T14:30:00Z"
      }
    ]
  };
}

async function fulfillJson(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function mockReviewApi(page: Page, server: MockServer): Promise<void> {
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());

    if (url.pathname === "/api/state") {
      const workspaceRevision = server.documentRevision === changedDocumentRevision ? 2 : 1;
      if (url.searchParams.get("since") === String(workspaceRevision)) {
        await fulfillJson(route, 200, {
          status: "unchanged",
          workspaceRevision
        });
        return;
      }
      await fulfillJson(route, 200, {
        status: "changed",
        workspaceRevision,
        documentCount: 1,
        initialDocumentPath: "README.md",
        navigation: [
          {
            kind: "document",
            name: "README.md",
            path: "README.md",
            sizeBytes: new TextEncoder().encode(source).length,
            availability: "ready",
            documentMetadataRevision: server.documentRevision,
            reviewMetadataRevision: server.reviewRevision
          }
        ],
        warnings: []
      });
      return;
    }

    if (url.pathname === "/api/document") {
      server.documentGets += 1;
      await fulfillJson(route, 200, {
        path: "README.md",
        revision: server.documentRevision,
        source
      });
      return;
    }

    if (url.pathname === "/api/review") {
      server.reviewGets += 1;
      await fulfillJson(route, 200, {
        path: "README.md",
        documentRevision: server.reviewDocumentRevision ?? server.documentRevision,
        reviewRevision: server.reviewRevision,
        threads: server.threads
      });
      return;
    }

    if (url.pathname === "/api/threads" && request.method() === "POST") {
      const body = request.postDataJSON() as Record<string, unknown>;
      server.posts.push({
        headers: request.headers(),
        body
      });
      if (server.create) {
        await server.create(route, body);
        return;
      }

      const anchor = body.anchor as Record<string, unknown>;
      const message = body.message as { body: string };
      const thread = {
        id: "thread_created",
        anchor,
        attachment:
          anchor.type === "text"
            ? {
                state: "attached",
                currentRange: anchor.range
              }
            : { state: "document" },
        status: "open",
        messages: [
          {
            id: "message_created",
            author: {
              type: "human",
              name: "Reviewer"
            },
            body: message.body,
            createdAt: "2026-07-29T00:00:00Z"
          }
        ]
      };
      server.reviewRevision = "f".repeat(64);
      server.threads.push(thread);
      await fulfillJson(route, 201, {
        documentRevision: server.documentRevision,
        reviewRevision: server.reviewRevision,
        durability: "durable",
        thread
      });
      return;
    }

    await fulfillJson(route, 404, {
      error: {
        code: "endpointNotFound",
        message: "Endpoint not found."
      }
    });
  });
}

async function textPoint(page: Page, needle: string): Promise<{ x: number; y: number }> {
  return page.locator(".markdown-body").evaluate((root, value) => {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && !node.textContent?.includes(value)) {
      node = walker.nextNode();
    }
    if (!node?.textContent) {
      throw new Error(`Missing ${value}`);
    }
    const start = node.textContent.indexOf(value);
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, start + value.length);
    const rect = range.getBoundingClientRect();
    return {
      x: rect.left + rect.width / 2,
      y: rect.top + rect.height / 2
    };
  }, needle);
}

test("loads separate document/text threads and targets overlapping non-mutating highlights", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewRevision,
    threads: [
      documentThread("thread_document", "<img src=x onerror=alert(1)>"),
      textThread("thread_alpha", "Alpha beta gamma", "First overlap"),
      textThread("thread_overlap", "beta gamma and overlap", "Second overlap"),
      {
        ...(textThread("thread_detached", "Alpha", "Detached message", "detached") as Record<
          string,
          unknown
        >),
        anchor: {
          type: "text",
          range: { start: 0, end: 7 },
          source: "Missing",
          text: "Missing"
        }
      }
    ],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);

  await expect(page.getByRole("heading", { name: "Document" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Text" })).toBeVisible();
  await expect(
    page.locator('.thread-card[data-thread-id="thread_detached"] .thread-metadata')
  ).toContainText("Detached");
  await expect(page.locator(".review-highlight")).toHaveCount(2);
  await expect(page.locator('.review-highlight[data-thread-id="thread_detached"]')).toHaveCount(0);
  await expect(page.locator(".review-panel img")).toHaveCount(0);
  await expect(page.getByText("<img src=x onerror=alert(1)>")).toHaveCount(0);

  const mappedLeavesBefore = await page
    .locator(".markdown-body [data-md-leaf]")
    .evaluateAll((leaves) =>
      leaves.map((leaf) => ({
        children: leaf.childNodes.length,
        text: leaf.textContent
      }))
    );

  const overlapPoint = await textPoint(page, "beta gamma");
  await page.mouse.click(overlapPoint.x, overlapPoint.y);
  await expect(page.locator('.thread-card.is-active[data-thread-id="thread_alpha"]')).toBeVisible();
  await page.mouse.click(overlapPoint.x, overlapPoint.y);
  await expect(
    page.locator('.thread-card.is-active[data-thread-id="thread_overlap"]')
  ).toBeVisible();

  await page.locator('.thread-card[data-thread-id="thread_alpha"] .thread-target').click();
  await expect(
    page.locator('.review-highlight.is-active[data-thread-id="thread_alpha"]')
  ).toBeVisible();

  expect(
    await page.locator(".markdown-body [data-md-leaf]").evaluateAll((leaves) =>
      leaves.map((leaf) => ({
        children: leaf.childNodes.length,
        text: leaf.textContent
      }))
    )
  ).toEqual(mappedLeavesBefore);
});

test("targets a persistent highlight after the document panel scrolls", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 260 });
  const server: MockServer = {
    documentRevision,
    reviewRevision,
    threads: [textThread("thread_alpha", "Alpha beta gamma", "Scrolled target")],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);
  await expect(page.locator('.review-highlight[data-thread-id="thread_alpha"]')).toBeVisible();

  const documentPanel = page.locator(".document-panel");
  await documentPanel.evaluate((panel) => {
    panel.scrollTop = 60;
  });
  await expect.poll(() => documentPanel.evaluate((panel) => panel.scrollTop)).toBeGreaterThan(0);
  const scrolledOverlapPoint = await textPoint(page, "beta gamma");
  await page.mouse.click(scrolledOverlapPoint.x, scrolledOverlapPoint.y);
  await expect(page.locator('.thread-card.is-active[data-thread-id="thread_alpha"]')).toBeVisible();
});

test("never places attachments from a persistently mismatched document revision", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewDocumentRevision: changedDocumentRevision,
    reviewRevision,
    threads: [textThread("thread_mismatched", "Alpha beta", "Do not place this")],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);

  await expect(
    page.getByRole("heading", {
      name: "Review comments changed while loading"
    })
  ).toBeVisible();
  expect(server.documentGets).toBeGreaterThanOrEqual(3);
  expect(server.reviewGets).toBe(server.documentGets);
  await expect(page.locator(".review-highlight")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toHaveCount(0);
});

test("creates a text thread from native pointer selection with a frozen exact anchor", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewRevision: null,
    threads: [],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);

  const point = await textPoint(page, "Alpha");
  await page.mouse.dblclick(point.x, point.y);
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toBeVisible();
  await page.getByRole("button", { name: "Comment on selected text" }).click();
  await expect(page.getByRole("heading", { name: "Comment on selection" })).toHaveCount(0);
  await expect(page.getByText("0 of 65536 bytes")).toHaveCount(0);
  await expect(page.locator(".review-highlight.is-draft")).toBeVisible();

  const textarea = page.getByRole("textbox", { name: "Comment" });
  await textarea.fill("Explain this opening word.");
  await textarea.press("Control+Enter");

  await expect(page.getByText("Explain this opening word.")).toBeVisible();
  await expect(page.locator('.review-highlight[data-thread-id="thread_created"]')).toBeVisible();
  expect(server.posts).toHaveLength(1);
  expect(server.posts[0]?.headers.authorization).toBeUndefined();
  expect(server.posts[0]?.headers["content-type"]).toContain("application/json");
  expect(server.posts[0]?.body).toEqual({
    documentPath: "README.md",
    expectedDocumentRevision: documentRevision,
    expectedReviewRevision: null,
    anchor: {
      type: "text",
      range: byteRange("Alpha"),
      source: "Alpha",
      text: "Alpha"
    },
    message: {
      body: "Explain this opening word."
    }
  });

  await page.goto(`/`);
  await expect(page.getByText("Explain this opening word.")).toBeVisible();
  const restored = page.locator('.review-highlight[data-thread-id="thread_created"]');
  await expect(restored).toBeVisible();
  const restoredRectangle = await restored.boundingBox();
  const targetRectangle = await page.locator(".markdown-body").evaluate((root) => {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && !node.textContent?.includes("Alpha")) {
      node = walker.nextNode();
    }
    if (!node?.textContent) {
      throw new Error("Missing Alpha");
    }
    const start = node.textContent.indexOf("Alpha");
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, start + "Alpha".length);
    const rectangle = range.getBoundingClientRect();
    return {
      x: rectangle.x,
      y: rectangle.y,
      width: rectangle.width,
      height: rectangle.height
    };
  });
  expect(restoredRectangle?.x).toBeCloseTo(targetRectangle.x, 0);
  expect(restoredRectangle?.y).toBeCloseTo(targetRectangle.y, 0);
  expect(restoredRectangle?.width).toBeCloseTo(targetRectangle.width, 0);
  expect(restoredRectangle?.height).toBeCloseTo(targetRectangle.height, 0);
});

test("creates document-level comments separately and cancels drafts with Escape", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewRevision: null,
    threads: [],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);

  await page.getByRole("button", { name: "Comment on document" }).click();
  await expect(page.getByRole("heading", { name: "Comment on document" })).toBeVisible();
  const textarea = page.getByRole("textbox", { name: "Comment" });
  await textarea.fill("Discard this document draft.");
  await textarea.press("Escape");
  await expect(textarea).toHaveCount(0);
  expect(server.posts).toHaveLength(0);

  await page.getByRole("button", { name: "Comment on document" }).click();
  await page.getByRole("textbox", { name: "Comment" }).fill("Add an introduction.");
  await page.getByRole("button", { name: "Save comment" }).click();

  await expect(page.getByText("Add an introduction.")).toBeVisible();
  expect(server.posts[0]?.body).toEqual({
    documentPath: "README.md",
    expectedDocumentRevision: documentRevision,
    expectedReviewRevision: null,
    anchor: {
      type: "document"
    },
    message: {
      body: "Add an introduction."
    }
  });
  await expect(page.locator('.review-highlight[data-thread-id="thread_created"]')).toHaveCount(0);
});

test("supports native keyboard extension and explains an unrepresentable boundary", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewRevision: null,
    threads: [],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);

  const point = await textPoint(page, "Alpha");
  await page.mouse.dblclick(point.x, point.y);
  await page.keyboard.down("Shift");
  for (let index = 0; index < 5; index += 1) {
    await page.keyboard.press("ArrowRight");
  }
  await page.keyboard.up("Shift");
  await expect
    .poll(() => page.evaluate(() => window.getSelection()?.toString()))
    .toBe("Alpha beta");
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toBeVisible();

  await page.evaluate(() => {
    const root = document.querySelector(".markdown-body");
    if (!root) {
      throw new Error("Missing document");
    }
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && !node.textContent?.includes("🙂")) {
      node = walker.nextNode();
    }
    if (!node?.textContent) {
      throw new Error("Missing entity rendering");
    }
    const offset = node.textContent.indexOf("🙂");
    const range = document.createRange();
    range.setStart(node, offset + 1);
    range.setEnd(node, offset + 2);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
  });
  await expect(page.getByText(/Adjust the selection to begin and end/u)).toBeVisible();
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toHaveCount(0);
});

test("retains a failed draft and frozen selection, then reloads after cancellation", async ({
  page
}) => {
  const server: MockServer = {
    documentRevision,
    reviewRevision: null,
    threads: [],
    documentGets: 0,
    reviewGets: 0,
    posts: []
  };
  server.create = async (route) => {
    server.documentRevision = changedDocumentRevision;
    await fulfillJson(route, 409, {
      error: {
        code: "documentChanged",
        message: "The document changed on disk. Your change was not submitted."
      },
      current: {
        documentRevision: changedDocumentRevision,
        reviewRevision: null
      }
    });
  };
  await mockReviewApi(page, server);
  await page.goto(`/`);
  await expect(page.getByRole("heading", { level: 1, name: "Review target" })).toBeVisible();
  const documentGetsBeforeConflict = server.documentGets;
  const reviewGetsBeforeConflict = server.reviewGets;

  const point = await textPoint(page, "Alpha");
  await page.mouse.dblclick(point.x, point.y);
  await page.getByRole("button", { name: "Comment on selected text" }).click();
  const textarea = page.getByRole("textbox", { name: "Comment" });
  await textarea.fill("Keep this draft through the conflict.");
  await page.getByRole("button", { name: "Save comment" }).click();

  await expect(page.getByRole("alert")).toContainText(
    "The document or review changed on disk. Reload before trying again."
  );
  await expect(page.getByRole("alert")).toContainText("draft and frozen selection have been kept");
  await expect(textarea).toHaveValue("Keep this draft through the conflict.");
  await expect(page.locator(".review-highlight.is-draft")).toBeVisible();
  expect(server.documentGets).toBe(documentGetsBeforeConflict);

  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(textarea).toHaveCount(0);
  await expect.poll(() => server.documentGets).toBe(documentGetsBeforeConflict + 1);
  await expect.poll(() => server.reviewGets).toBe(reviewGetsBeforeConflict + 1);
});
