import { expect, test, type Page, type Route } from "@playwright/test";

const documentRevision = "d0820a0afd1e1aa6b8bbf91c8f6915e6d544eec8be1c032f7779a5e6a6b7b908";
const source = `# Review workflow

Alpha beta gamma.

Second paragraph.
`;

interface MockMessage {
  id: string;
  author: {
    type: "human" | "agent";
    name: string;
  };
  body: string;
  createdAt: string;
  editedAt?: string;
}

interface MockThread {
  id: string;
  anchor:
    | {
        type: "document";
      }
    | {
        type: "text";
        range: { start: number; end: number };
        source: string;
        text: string;
      };
  attachment:
    | {
        state: "document";
      }
    | {
        state: "attached";
        currentRange: { start: number; end: number };
      };
  status: "open" | "handled" | "resolved";
  messages: MockMessage[];
}

interface MockServer {
  threads: MockThread[];
  failNextMutation: boolean;
  observedRoutes: string[];
  mutationNumber: number;
  reviewGets: number;
}

function message(id: string, body: string, authorType: "human" | "agent" = "human"): MockMessage {
  return {
    id,
    author: {
      type: authorType,
      name: authorType === "human" ? "External reviewer" : "Codex"
    },
    body,
    createdAt: "2026-07-28T14:30:00Z"
  };
}

function documentThread(
  id: string,
  status: MockThread["status"],
  messages: MockMessage[]
): MockThread {
  return {
    id,
    anchor: { type: "document" },
    attachment: { state: "document" },
    status,
    messages
  };
}

function textThread(
  id: string,
  selected: string,
  status: MockThread["status"],
  messages: MockMessage[]
): MockThread {
  const start = source.indexOf(selected);
  if (start < 0) {
    throw new Error(`Missing source text: ${selected}`);
  }
  const range = { start, end: start + selected.length };
  return {
    id,
    anchor: {
      type: "text",
      range,
      source: selected,
      text: selected
    },
    attachment: {
      state: "attached",
      currentRange: range
    },
    status,
    messages
  };
}

function decodeOpaqueID(segment: string): string {
  if (!segment.startsWith("~")) {
    throw new Error(`Invalid opaque segment: ${segment}`);
  }
  const base64 = segment
    .slice(1)
    .replaceAll("-", "+")
    .replaceAll("_", "/")
    .padEnd(Math.ceil((segment.length - 1) / 4) * 4, "=");
  return Buffer.from(base64, "base64").toString("utf8");
}

async function fulfillJson(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function mockWorkflowAPI(page: Page, server: MockServer): Promise<void> {
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/state") {
      const workspaceRevision = server.failNextMutation ? 1 : 2;
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
            documentMetadataRevision: documentRevision,
            reviewMetadataRevision: (server.failNextMutation ? "a" : "b").repeat(64)
          }
        ],
        warnings: []
      });
      return;
    }
    if (url.pathname === "/api/document") {
      await fulfillJson(route, 200, {
        path: "README.md",
        revision: documentRevision,
        source
      });
      return;
    }
    if (url.pathname === "/api/review") {
      server.reviewGets += 1;
      await fulfillJson(route, 200, {
        path: "README.md",
        documentRevision,
        reviewRevision: "a".repeat(64),
        threads: server.threads
      });
      return;
    }

    server.observedRoutes.push(`${request.method()} ${url.pathname}`);
    if (server.failNextMutation) {
      server.failNextMutation = false;
      await fulfillJson(route, 409, {
        error: {
          code: "reviewChanged",
          message: "The review changed on disk. Your change was not submitted.",
          requestId: "request-review-change"
        },
        current: {
          documentRevision,
          reviewRevision: "b".repeat(64)
        }
      });
      return;
    }

    const parts = url.pathname.split("/");
    let affectedThread: MockThread | undefined;
    if (
      request.method() === "POST" &&
      parts[1] === "api" &&
      parts[2] === "threads" &&
      parts[4] === "messages"
    ) {
      const threadID = decodeOpaqueID(parts[3] ?? "");
      affectedThread = server.threads.find((thread) => thread.id === threadID);
      const body = request.postDataJSON() as { message: { body: string } };
      if (affectedThread) {
        affectedThread.messages.push(
          message(`message_reply_${String(server.mutationNumber)}`, body.message.body)
        );
        affectedThread.status = "open";
      }
    } else if (request.method() === "PATCH" && parts[1] === "api" && parts[2] === "messages") {
      const messageID = decodeOpaqueID(parts[3] ?? "");
      const body = request.postDataJSON() as { message: { body: string } };
      affectedThread = server.threads.find((thread) =>
        thread.messages.some((item) => item.id === messageID)
      );
      const target = affectedThread?.messages.find((item) => item.id === messageID);
      if (target) {
        target.body = body.message.body;
        target.editedAt = "2026-07-29T01:00:00Z";
      }
    } else if (
      request.method() === "PATCH" &&
      parts[1] === "api" &&
      parts[2] === "threads" &&
      parts[4] === "status"
    ) {
      const threadID = decodeOpaqueID(parts[3] ?? "");
      const body = request.postDataJSON() as { status: "open" | "resolved" };
      affectedThread = server.threads.find((thread) => thread.id === threadID);
      if (affectedThread) {
        affectedThread.status = body.status;
      }
    } else if (
      request.method() === "DELETE" &&
      parts[1] === "api" &&
      parts[2] === "threads" &&
      parts.length === 4
    ) {
      const threadID = decodeOpaqueID(parts[3] ?? "");
      server.threads = server.threads.filter((thread) => thread.id !== threadID);
      server.mutationNumber += 1;
      await fulfillJson(route, 200, {
        documentRevision,
        reviewRevision: "e".repeat(64),
        durability: "durable",
        deletedThreadId: threadID
      });
      return;
    }

    if (!affectedThread) {
      await fulfillJson(route, 422, {
        error: {
          code: "invalidReviewOperation",
          message: "The operation is not valid.",
          requestId: "request-invalid"
        }
      });
      return;
    }
    server.mutationNumber += 1;
    await fulfillJson(route, request.method() === "POST" ? 201 : 200, {
      documentRevision,
      reviewRevision: "e".repeat(64),
      durability: "durable",
      thread: affectedThread
    });
  });
}

test("completes the human lifecycle while preserving direct-file states and focus", async ({
  page
}) => {
  const server: MockServer = {
    threads: [
      documentThread("thread_handled", "handled", [
        message("message_external", "Externally authored human message")
      ]),
      textThread("thread_resolved", "Alpha", "resolved", [
        message("message_review", "Initial review"),
        message("message_agent", "Direct agent reply", "agent")
      ]),
      documentThread("thread_delete", "open", [message("message_delete", "Remove this thread")])
    ],
    failNextMutation: false,
    observedRoutes: [],
    mutationNumber: 1,
    reviewGets: 0
  };
  await mockWorkflowAPI(page, server);
  await page.goto(`/`);

  const handled = page.locator('.thread-card[data-thread-id="thread_handled"]');
  const resolved = page.locator('.thread-card[data-thread-id="thread_resolved"]');
  await expect(handled).toBeVisible();
  await expect(resolved).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Document" })).toBeVisible();

  await page.getByRole("checkbox", { name: "Resolved" }).check();
  await expect(resolved).toBeVisible();
  await expect(resolved.locator(".thread-metadata")).toContainText("Resolved");
  await expect(resolved.getByText("Direct agent reply")).toBeVisible();
  await expect(resolved.getByRole("button", { name: "Delete thread" })).toHaveCount(0);

  await handled
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await handled.getByRole("button", { name: "Edit message 1" }).click();
  const edit = handled.getByRole("textbox", { name: "Edit message" });
  await edit.fill("Externally authored message, edited by the reviewer");
  await edit.press("Control+Enter");
  await expect(
    handled.getByText("Externally authored message, edited by the reviewer")
  ).toBeVisible();
  await expect(handled.locator(".message-author")).toContainText("edited");
  await expect(handled.locator(".thread-target")).toBeFocused();

  await handled.getByRole("button", { name: "Reply" }).click();
  const reply = handled.getByRole("textbox", { name: "Reply" });
  await reply.fill("Human follow-up");
  await reply.press("Control+Enter");
  await expect(handled.getByText("Human follow-up")).toBeVisible();
  await expect(handled.locator(".thread-metadata")).toContainText("Open");

  await handled.getByRole("button", { name: "Resolve" }).click();
  await expect(handled.locator(".thread-metadata")).toContainText("Resolved");
  await handled
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await handled.getByRole("button", { name: "Reopen" }).click();
  await expect(handled.locator(".thread-metadata")).toContainText("Open");

  const deleted = page.locator('.thread-card[data-thread-id="thread_delete"]');
  await deleted
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await deleted.getByRole("button", { name: "Delete thread" }).click();
  await deleted.getByRole("button", { name: "Cancel" }).click();
  await expect(
    deleted.locator(".thread-card-header").getByRole("button", { name: /More actions for/u })
  ).toBeFocused();
  await deleted
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await deleted.getByRole("button", { name: "Delete thread" }).click();
  await deleted.getByRole("button", { name: "Delete thread" }).click();
  await expect(deleted).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Comment on document" })).toBeFocused();

  expect(server.observedRoutes).toContain("PATCH /api/messages/~bWVzc2FnZV9leHRlcm5hbA");
  expect(server.observedRoutes).toContain("POST /api/threads/~dGhyZWFkX2hhbmRsZWQ/messages");
});

test("filters and navigates active highlights while rendering reduced safe message Markdown", async ({
  page
}) => {
  const hostileBody = `# Reduced heading

**Strong** and \`code\`, [safe](https://example.com/review), [unsafe](javascript:alert(1)).

![private](file:///etc/passwd)

<script>window.reviewPwned = true</script>`;
  const server: MockServer = {
    threads: [
      textThread("thread_text", "Alpha", "open", [message("message_text", hostileBody)]),
      documentThread("thread_document", "handled", [message("message_document", "Document note")]),
      documentThread("thread_hidden", "resolved", [message("message_hidden", "Resolved note")])
    ],
    failNextMutation: false,
    observedRoutes: [],
    mutationNumber: 1,
    reviewGets: 0
  };
  await mockWorkflowAPI(page, server);
  await page.goto(`/`);

  await expect(page.getByText("Resolved note")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Document" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Text" })).toBeVisible();

  const textCard = page.locator('.thread-card[data-thread-id="thread_text"]');
  await expect(textCard.locator("strong")).toHaveText("Strong");
  await expect(textCard.locator("code")).toContainText("code");
  await expect(textCard.getByRole("link", { name: "safe" })).toHaveAttribute(
    "href",
    "https://example.com/review"
  );
  await expect(textCard.getByRole("link", { name: "unsafe" })).toHaveCount(0);
  await expect(textCard.locator("img, script, h1, h2, h3, h4, h5, h6")).toHaveCount(0);
  expect(
    await page.evaluate(
      () => typeof (window as typeof window & { reviewPwned?: unknown }).reviewPwned
    )
  ).toBe("undefined");

  await page.getByRole("checkbox", { name: "Open" }).uncheck();
  await expect(textCard).toHaveCount(0);
  await page.getByRole("checkbox", { name: "Handled" }).uncheck();
  await expect(page.getByText("No comments match the selected status filters.")).toBeVisible();
  await page.getByRole("checkbox", { name: "Resolved" }).check();
  await expect(page.getByText("Resolved note")).toBeVisible();
});

test("keeps reply and edit drafts after revision conflicts and permits a keyboard retry", async ({
  page
}) => {
  const server: MockServer = {
    threads: [
      documentThread("thread_failure", "open", [
        message("message_failure", "Original external message")
      ])
    ],
    failNextMutation: true,
    observedRoutes: [],
    mutationNumber: 1,
    reviewGets: 0
  };
  await mockWorkflowAPI(page, server);
  await page.goto(`/`);

  const card = page.locator('.thread-card[data-thread-id="thread_failure"]');
  await card.getByRole("button", { name: "Reply" }).click();
  const reply = card.getByRole("textbox", { name: "Reply" });
  await reply.fill("Draft survives conflict");
  await reply.press("Control+Enter");
  await expect(card.getByRole("alert")).toContainText("draft has been kept");
  await expect(reply).toHaveValue("Draft survives conflict");
  await expect.poll(() => server.reviewGets).toBeGreaterThan(1);

  await reply.press("Control+Enter");
  await expect(card.getByText("Draft survives conflict")).toBeVisible();

  server.failNextMutation = true;
  await card
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await card.getByRole("button", { name: "Edit message 1" }).click();
  const edit = card.getByRole("textbox", { name: "Edit message" });
  await edit.fill("Edited draft survives too");
  await edit.press("Control+Enter");
  await expect(card.getByRole("alert")).toContainText("draft has been kept");
  await expect(edit).toHaveValue("Edited draft survives too");
  await edit.press("Escape");
  await expect(card.getByRole("textbox", { name: "Edit message" })).toHaveCount(0);
  await expect(card.locator(".thread-target")).toBeFocused();
});

test("describes an oversized reply to assistive technology", async ({ page }) => {
  const server: MockServer = {
    threads: [documentThread("thread_limit", "open", [message("message_limit", "A comment")])],
    failNextMutation: false,
    observedRoutes: [],
    mutationNumber: 1,
    reviewGets: 0
  };
  await mockWorkflowAPI(page, server);
  await page.goto(`/`);

  const card = page.locator('.thread-card[data-thread-id="thread_limit"]');
  await card.getByRole("button", { name: "Reply" }).click();
  const reply = card.getByRole("textbox", { name: "Reply" });
  await reply.fill("x".repeat(65_537));
  await expect(reply).toHaveAttribute("aria-invalid", "true");
  const limitID = await reply.getAttribute("aria-describedby");
  expect(limitID).not.toBeNull();
  await expect(card.locator(`#${limitID ?? "missing"}`)).toHaveText(
    "Messages must be no more than 64 KiB of UTF-8."
  );
});
