import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { expect, test, type Page } from "@playwright/test";

function serverEnvironment(): {
  baseURL: string;
  workspace: string;
} {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  const workspace = process.env.MDREVIEW_GO_SERVER_WORKSPACE;
  if (!baseURL || !workspace) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL, workspace };
}

function projectLabel(projectName: string): string {
  return projectName === "firefox" ? "Firefox" : "Chromium";
}

async function openWorkspace(page: Page): Promise<void> {
  const { baseURL } = serverEnvironment();
  // A fragment-only navigation on the already-clean URL does not reload the
  // document. Leave the origin first so this exercises a fresh application
  // Open the normal loopback URL.
  await page.goto("about:blank");
  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
}

async function selectDocument(page: Page, path: string, heading: string): Promise<void> {
  await page.getByRole("button", { name: new RegExp(path.replace(".", "\\."), "u") }).click();
  await expect(page.getByRole("heading", { level: 1, name: heading })).toBeVisible();
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
    const rectangle = range.getBoundingClientRect();
    return {
      x: rectangle.left + rectangle.width / 2,
      y: rectangle.top + rectangle.height / 2
    };
  }, needle);
}

async function textRectangle(
  page: Page,
  needle: string
): Promise<{ x: number; y: number; width: number; height: number }> {
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
    const rectangle = range.getBoundingClientRect();
    return {
      x: rectangle.x,
      y: rectangle.y,
      width: rectangle.width,
      height: rectangle.height
    };
  }, needle);
}

test("real pointer selection creates an adjacent sidecar and restores its exact highlight", async ({
  page
}, testInfo) => {
  const { workspace } = serverEnvironment();
  const label = projectLabel(testInfo.project.name);
  const documentPath = `text-${testInfo.project.name}.md`;
  const message = `Explain the durable anchor in ${testInfo.project.name}.`;

  await openWorkspace(page);
  await selectDocument(page, documentPath, `${label} text review`);
  const point = await textPoint(page, "durable");
  await page.mouse.dblclick(point.x, point.y);
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toBeVisible();
  await page.getByRole("button", { name: "Comment on selected text" }).click();
  const textarea = page.getByRole("textbox", { name: "Comment" });
  await textarea.fill(message);
  await textarea.press("Control+Enter");

  await expect(page.getByText(message)).toBeVisible();
  const createdHighlight = page.locator(".review-highlight").first();
  await expect(createdHighlight).toBeVisible();

  const markdown = await readFile(join(workspace, documentPath));
  const sidecar = JSON.parse(
    await readFile(join(workspace, `${documentPath}.review.json`), "utf8")
  ) as {
    schemaVersion: number;
    threads: Array<{
      anchor: {
        type: string;
        range: { start: number; end: number };
        source: string;
        text: string;
      };
      attachment?: unknown;
      messages: Array<{ body: string }>;
    }>;
  };
  expect(sidecar.schemaVersion).toBe(1);
  expect(sidecar.threads).toHaveLength(1);
  const persisted = sidecar.threads[0];
  expect(persisted?.anchor.type).toBe("text");
  expect(persisted?.anchor.source).toBe("durable");
  expect(persisted?.anchor.text).toBe("durable");
  expect(
    markdown.subarray(persisted?.anchor.range.start, persisted?.anchor.range.end).toString("utf8")
  ).toBe("durable");
  expect(persisted?.attachment).toBeUndefined();
  expect(persisted?.messages[0]?.body).toBe(message);

  await openWorkspace(page);
  await selectDocument(page, documentPath, `${label} text review`);
  await expect(page.getByText(message)).toBeVisible();
  const restoredHighlight = page.locator(".review-highlight").first();
  await expect(restoredHighlight).toBeVisible();
  const restoredRectangle = await restoredHighlight.boundingBox();
  const targetRectangle = await textRectangle(page, "durable");
  expect(restoredRectangle?.x).toBeCloseTo(targetRectangle.x, 0);
  expect(restoredRectangle?.y).toBeCloseTo(targetRectangle.y, 0);
  expect(restoredRectangle?.width).toBeCloseTo(targetRectangle.width, 0);
  expect(restoredRectangle?.height).toBeCloseTo(targetRectangle.height, 0);
});

test("real keyboard extension rebases one exact moved source and preserves the frozen draft", async ({
  page
}, testInfo) => {
  const { workspace } = serverEnvironment();
  const label = projectLabel(testInfo.project.name);
  const documentPath = `moved-${testInfo.project.name}.md`;
  const documentFile = join(workspace, documentPath);
  const sidecarFile = `${documentFile}.review.json`;
  const selectedSource = "exact selected";
  const message = `Keep the moved selection for ${testInfo.project.name}.`;

  await openWorkspace(page);
  await selectDocument(page, documentPath, `${label} moved selection`);
  const point = await textPoint(page, "exact");
  await page.mouse.dblclick(point.x, point.y);
  await page.keyboard.down("Shift");
  for (let index = 0; index < 9; index += 1) {
    await page.keyboard.press("ArrowRight");
  }
  await page.keyboard.up("Shift");
  await expect
    .poll(() => page.evaluate(() => window.getSelection()?.toString()))
    .toBe(selectedSource);
  await page.getByRole("button", { name: "Comment on selected text" }).click();
  const textarea = page.getByRole("textbox", { name: "Comment" });
  await textarea.fill(message);

  const original = await readFile(documentFile, "utf8");
  const moved = `A new leading paragraph moves the anchor.\n\n${original}`;
  await writeFile(documentFile, moved, "utf8");
  await textarea.press("Control+Enter");

  await expect(page.getByText(message)).toBeVisible();
  const sidecar = JSON.parse(await readFile(sidecarFile, "utf8")) as {
    threads: Array<{
      anchor: {
        range: { start: number; end: number };
        source: string;
      };
    }>;
  };
  const anchor = sidecar.threads[0]?.anchor;
  const expectedStart = Buffer.byteLength(moved.slice(0, moved.indexOf(selectedSource)));
  expect(anchor?.source).toBe(selectedSource);
  expect(anchor?.range).toEqual({
    start: expectedStart,
    end: expectedStart + Buffer.byteLength(selectedSource)
  });

  await openWorkspace(page);
  await selectDocument(page, documentPath, `${label} moved selection`);
  await expect(page.getByText(message)).toBeVisible();
  await expect(page.locator(".review-highlight").first()).toBeVisible();
});

test("document comments persist separately and invalid sidecars remain read-only", async ({
  page
}, testInfo) => {
  const { workspace } = serverEnvironment();
  const label = projectLabel(testInfo.project.name);
  const documentPath = `document-${testInfo.project.name}.md`;
  const message = `Add a document summary for ${testInfo.project.name}.`;

  await openWorkspace(page);
  await selectDocument(page, documentPath, `${label} document review`);
  await page.getByRole("button", { name: "Comment on document" }).click();
  const discarded = page.getByRole("textbox", { name: "Comment" });
  await discarded.fill("Discard this draft.");
  await discarded.press("Escape");
  await expect(discarded).toHaveCount(0);

  await page.getByRole("button", { name: "Comment on document" }).click();
  await page.getByRole("textbox", { name: "Comment" }).fill(message);
  await page.getByRole("button", { name: "Save comment" }).click();
  await expect(page.getByText(message)).toBeVisible();
  await expect(page.locator(".review-highlight")).toHaveCount(0);

  const sidecar = JSON.parse(
    await readFile(join(workspace, `${documentPath}.review.json`), "utf8")
  ) as {
    threads: Array<{ anchor: { type: string }; messages: Array<{ body: string }> }>;
  };
  expect(sidecar.threads[0]?.anchor).toEqual({ type: "document" });
  expect(sidecar.threads[0]?.messages[0]?.body).toBe(message);

  await selectDocument(page, "invalid-review.md", "Invalid review");
  await expect(page.getByRole("heading", { name: "The review sidecar is invalid" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Comment on document" })).toHaveCount(0);
  const invalidPoint = await textPoint(page, "Markdown");
  await page.mouse.dblclick(invalidPoint.x, invalidPoint.y);
  await expect(page.getByRole("button", { name: "Comment on selected text" })).toHaveCount(0);
});
