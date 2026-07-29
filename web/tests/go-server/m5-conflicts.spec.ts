import { readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";

import {
  expect,
  test,
  type APIRequestContext,
  type BrowserContext,
  type Page,
  type Request
} from "@playwright/test";

interface ServerEnvironment {
  baseURL: string;
  workspace: string;
}

interface ReviewSnapshot {
  reviewRevision: string;
  targets: {
    threads: Record<string, string>;
    messages: Record<string, string>;
  };
}

interface MutationBody {
  targetFingerprint?: string;
}

type EditorKind = "reply" | "edit";

const threadID = "thread_two_tab_target";
const messageID = "message_two_tab_target";
const initialBody = "Initial two-tab conflict target.";
const externalBody = "Externally changed two-tab conflict target.";

function serverEnvironment(): ServerEnvironment {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  const workspace = process.env.MDREVIEW_GO_SERVER_WORKSPACE;
  if (!baseURL || !workspace) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL, workspace };
}

function fixtureSidecar(): string {
  return `${JSON.stringify(
    {
      schemaVersion: 1,
      threads: [
        {
          id: threadID,
          anchor: {
            type: "document"
          },
          status: "open",
          messages: [
            {
              id: messageID,
              author: {
                type: "human",
                name: "Reviewer"
              },
              body: initialBody,
              createdAt: "2026-07-29T09:00:00Z"
            }
          ]
        }
      ]
    },
    null,
    2
  )}\n`;
}

function replaceExactly(content: string, before: string, after: string): string {
  const first = content.indexOf(before);
  expect(first, `fixture text ${JSON.stringify(before)} is missing`).toBeGreaterThanOrEqual(0);
  expect(content.indexOf(before, first + before.length), "fixture replacement is ambiguous").toBe(
    -1
  );
  return content.slice(0, first) + after + content.slice(first + before.length);
}

async function readReview(
  request: APIRequestContext,
  environment: ServerEnvironment,
  documentPath: string
): Promise<ReviewSnapshot> {
  const response = await request.get(
    `${environment.baseURL}/api/review?path=${encodeURIComponent(documentPath)}`,
    {
      headers: {}
    }
  );
  expect(response.status()).toBe(200);
  return (await response.json()) as ReviewSnapshot;
}

function requiredFingerprint(
  targets: Record<string, string>,
  targetID: string,
  label: string
): string {
  const fingerprint = targets[targetID];
  expect(fingerprint, `${label} fingerprint is missing`).toMatch(/^[0-9a-f]{64}$/u);
  if (!fingerprint) {
    throw new Error(`${label} fingerprint is missing`);
  }
  return fingerprint;
}

async function openFixture(
  context: BrowserContext,
  environment: ServerEnvironment,
  documentPath: string,
  heading: string
): Promise<Page> {
  const page = await context.newPage();
  await page.goto(`${environment.baseURL}/`);
  const navigationButton = page.getByRole("button", {
    name: new RegExp(documentPath.replaceAll(".", "\\."), "u")
  });
  await expect(navigationButton).toBeVisible({ timeout: 10_000 });
  await navigationButton.click();
  await expect(page.getByRole("heading", { level: 1, name: heading })).toBeVisible();
  await expect(page.locator(`[data-thread-id="${threadID}"]`)).toBeVisible();
  return page;
}

function mutationRequest(kind: EditorKind, request: Request): boolean {
  const url = new URL(request.url());
  return kind === "reply"
    ? request.method() === "POST" && /^\/api\/threads\/[^/]+\/messages$/u.test(url.pathname)
    : request.method() === "PATCH" && /^\/api\/messages\/[^/]+$/u.test(url.pathname);
}

function observeMutationBodies(page: Page, kind: EditorKind): MutationBody[] {
  const bodies: MutationBody[] = [];
  page.on("request", (request) => {
    if (!mutationRequest(kind, request)) {
      return;
    }
    bodies.push(request.postDataJSON() as MutationBody);
  });
  return bodies;
}

async function openEditor(page: Page, kind: EditorKind, draft: string): Promise<void> {
  const card = page.locator(`[data-thread-id="${threadID}"]`);
  if (kind === "reply") {
    await card.getByRole("button", { name: "Reply" }).click();
    await card.getByRole("textbox", { name: "Reply" }).fill(draft);
    return;
  }
  await card
    .locator(".thread-card-header")
    .getByRole("button", { name: /More actions for/u })
    .click();
  await card.getByRole("button", { name: "Edit message 1" }).click();
  await card.getByRole("textbox", { name: "Edit message" }).fill(draft);
}

function editor(page: Page, kind: EditorKind) {
  return page.getByRole("textbox", {
    name: kind === "reply" ? "Reply" : "Edit message"
  });
}

for (const kind of ["reply", "edit"] as const) {
  test(`two independent tabs retain stale ${kind} drafts after real sidecar target conflicts`, async ({
    browser,
    request
  }, testInfo) => {
    const environment = serverEnvironment();
    const label = testInfo.project.name === "firefox" ? "Firefox" : "Chromium";
    const documentPath = `m5-two-tab-${kind}-${testInfo.project.name}.md`;
    const documentFile = join(environment.workspace, documentPath);
    const sidecarFile = join(environment.workspace, `${documentPath}.review.json`);
    const heading = `${label} two-tab ${kind} conflict`;
    const initialSidecar = fixtureSidecar();
    const externalSidecar = replaceExactly(initialSidecar, initialBody, externalBody);
    const contexts: BrowserContext[] = [];

    try {
      await writeFile(
        documentFile,
        `# ${heading}\n\nThis fixture exercises a real compiled-server conflict.\n`,
        "utf8"
      );
      await writeFile(sidecarFile, initialSidecar, "utf8");

      const firstContext = await browser.newContext();
      const secondContext = await browser.newContext();
      contexts.push(firstContext, secondContext);
      const [firstPage, secondPage] = await Promise.all([
        openFixture(firstContext, environment, documentPath, heading),
        openFixture(secondContext, environment, documentPath, heading)
      ]);
      const firstMutationBodies = observeMutationBodies(firstPage, kind);
      const secondMutationBodies = observeMutationBodies(secondPage, kind);
      const firstDraft = `First tab exact ${kind} draft`;
      const secondDraft = `Second tab independent ${kind} draft`;

      const initialReview = await readReview(request, environment, documentPath);
      const initialFingerprint =
        kind === "reply"
          ? requiredFingerprint(initialReview.targets.threads, threadID, "initial thread")
          : requiredFingerprint(initialReview.targets.messages, messageID, "initial message");

      await Promise.all([
        openEditor(firstPage, kind, firstDraft),
        openEditor(secondPage, kind, secondDraft)
      ]);
      await expect(editor(firstPage, kind)).toHaveValue(firstDraft);
      await expect(editor(secondPage, kind)).toHaveValue(secondDraft);

      await writeFile(sidecarFile, externalSidecar, "utf8");
      await expect(firstPage.getByText(externalBody)).toBeVisible({ timeout: 10_000 });
      await expect(secondPage.getByText(externalBody)).toBeVisible({ timeout: 10_000 });
      await expect(editor(firstPage, kind)).toHaveValue(firstDraft);
      await expect(editor(secondPage, kind)).toHaveValue(secondDraft);

      const changedReview = await readReview(request, environment, documentPath);
      const changedFingerprint =
        kind === "reply"
          ? requiredFingerprint(changedReview.targets.threads, threadID, "changed thread")
          : requiredFingerprint(changedReview.targets.messages, messageID, "changed message");
      expect(changedFingerprint).not.toBe(initialFingerprint);
      expect(changedReview.reviewRevision).not.toBe(initialReview.reviewRevision);

      await editor(firstPage, kind).press("Control+Enter");
      await expect(
        firstPage.getByRole("alert").filter({ hasText: "This review item changed on disk." })
      ).toBeVisible();
      await expect(editor(firstPage, kind)).toHaveValue(firstDraft);
      await expect(editor(secondPage, kind)).toHaveValue(secondDraft);

      await editor(secondPage, kind).press("Control+Enter");
      await expect(
        secondPage.getByRole("alert").filter({ hasText: "This review item changed on disk." })
      ).toBeVisible();
      await expect(editor(firstPage, kind)).toHaveValue(firstDraft);
      await expect(editor(secondPage, kind)).toHaveValue(secondDraft);

      expect(firstMutationBodies).toHaveLength(1);
      expect(secondMutationBodies).toHaveLength(1);
      expect(firstMutationBodies[0]?.targetFingerprint).toBe(initialFingerprint);
      expect(secondMutationBodies[0]?.targetFingerprint).toBe(initialFingerprint);
      expect(firstMutationBodies[0]?.targetFingerprint).not.toBe(changedFingerprint);
      expect(secondMutationBodies[0]?.targetFingerprint).not.toBe(changedFingerprint);

      const finalSidecar = await readFile(sidecarFile, "utf8");
      expect(finalSidecar).toBe(externalSidecar);
      expect(finalSidecar).not.toContain(firstDraft);
      expect(finalSidecar).not.toContain(secondDraft);
    } finally {
      await Promise.allSettled(contexts.map((context) => context.close()));
      await rm(sidecarFile, { force: true });
      await rm(documentFile, { force: true });
    }
  });
}
