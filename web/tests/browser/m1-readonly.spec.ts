import { expect, test, type Page, type Route } from "@playwright/test";

const revision = "d0820a0afd1e1aa6b8bbf91c8f6915e6d544eec8be1c032f7779a5e6a6b7b908";

type Reply =
  | {
      status: 200;
      source: string;
    }
  | {
      status: 404 | 413 | 422 | 500;
      code: "documentNotFound" | "documentTooLarge" | "documentInvalidUtf8" | "documentUnavailable";
      message: string;
    };

interface MockWorkspace {
  state: unknown;
  documents: ReadonlyMap<string, Reply>;
}

interface ObservedRequest {
  url: string;
  authorization: string | undefined;
}

function errorBody(reply: Exclude<Reply, { status: 200 }>): unknown {
  return {
    error: {
      code: reply.code,
      message: reply.message,
      requestId: `request-${String(reply.status)}`
    }
  };
}

async function fulfillJson(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function mockApi(page: Page, workspace: MockWorkspace): Promise<ObservedRequest[]> {
  const observed: ObservedRequest[] = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    observed.push({
      url: request.url(),
      authorization: request.headers()["authorization"]
    });

    if (url.pathname === "/api/state") {
      if (url.searchParams.has("since")) {
        await fulfillJson(route, 200, {
          status: "unchanged",
          workspaceRevision: 1
        });
        return;
      }
      await fulfillJson(route, 200, workspace.state);
      return;
    }

    if (url.pathname === "/api/document") {
      const path = url.searchParams.get("path");
      const reply = path ? workspace.documents.get(path) : undefined;
      if (!path || !reply) {
        await fulfillJson(route, 404, {
          error: {
            code: "documentNotFound",
            message: "This Markdown document was not found.",
            requestId: "request-missing"
          }
        });
        return;
      }
      if (reply.status === 200) {
        await fulfillJson(route, 200, {
          path,
          revision,
          source: reply.source
        });
        return;
      }
      await fulfillJson(route, reply.status, errorBody(reply));
      return;
    }

    if (url.pathname === "/api/review") {
      const path = url.searchParams.get("path");
      await fulfillJson(route, 200, {
        path,
        documentRevision: revision,
        reviewRevision: null,
        threads: []
      });
      return;
    }

    await fulfillJson(route, 404, {
      error: {
        code: "endpointNotFound",
        message: "Endpoint not found.",
        requestId: "request-endpoint"
      }
    });
  });
  return observed;
}

function fullWorkspace(): MockWorkspace {
  return {
    state: {
      status: "changed",
      workspaceRevision: 1,
      documentCount: 6,
      initialDocumentPath: "README.md",
      navigation: [
        {
          kind: "directory",
          name: "docs",
          path: "docs",
          children: [
            {
              kind: "document",
              name: "deleted.md",
              path: "docs/deleted.md",
              sizeBytes: 10,
              availability: "ready",
              documentMetadataRevision: revision,
              reviewMetadataRevision: null
            },
            {
              kind: "document",
              name: "guide.md",
              path: "docs/guide.md",
              sizeBytes: 100,
              availability: "ready",
              documentMetadataRevision: revision,
              reviewMetadataRevision: null
            },
            {
              kind: "document",
              name: "invalid.md",
              path: "docs/invalid.md",
              sizeBytes: 10,
              availability: "ready",
              documentMetadataRevision: revision,
              reviewMetadataRevision: null
            },
            {
              kind: "document",
              name: "large.md",
              path: "docs/large.md",
              sizeBytes: 8_388_609,
              availability: "tooLarge",
              documentMetadataRevision: revision,
              reviewMetadataRevision: null
            },
            {
              kind: "document",
              name: "unavailable.md",
              path: "docs/unavailable.md",
              sizeBytes: 10,
              availability: "ready",
              documentMetadataRevision: revision,
              reviewMetadataRevision: null
            }
          ]
        },
        {
          kind: "document",
          name: "README.md",
          path: "README.md",
          sizeBytes: 400,
          availability: "ready",
          documentMetadataRevision: revision,
          reviewMetadataRevision: null
        }
      ],
      warnings: [
        {
          path: "vendor/.gitignore",
          code: "ignoreFileTooLarge",
          message: "This ignore file exceeds 1 MiB and was skipped."
        }
      ]
    },
    documents: new Map([
      [
        "README.md",
        {
          status: 200,
          source: `# Workspace

Welcome to the **read-only** workspace.

## Overview

[Open guide](docs/guide.md)

[Jump to overview](#overview)

[External help](https://example.com/help)

[Unsafe action](javascript:alert(1))

[Missing document](missing.md)
`
        }
      ],
      [
        "docs/guide.md",
        {
          status: 200,
          source: `# Guide

Use the nested guide.

[Back to readme](../README.md)
`
        }
      ],
      [
        "docs/invalid.md",
        {
          status: 422,
          code: "documentInvalidUtf8",
          message: "This Markdown file is not valid UTF-8."
        }
      ],
      [
        "docs/deleted.md",
        {
          status: 404,
          code: "documentNotFound",
          message: "This Markdown document was not found."
        }
      ],
      [
        "docs/unavailable.md",
        {
          status: 500,
          code: "documentUnavailable",
          message: "This Markdown document is unavailable."
        }
      ]
    ])
  };
}

test("loads the complete read-only shell and supports filtered keyboard navigation", async ({
  page
}) => {
  const requests = await mockApi(page, fullWorkspace());
  await page.goto(`/`);

  await expect(page).toHaveURL("http://127.0.0.1:4173/");
  await expect(page.getByRole("complementary", { name: "Files" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Comments", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Workspace" })).toBeVisible();
  await expect(page.getByRole("button", { name: /README\.md/u })).toHaveAttribute(
    "aria-current",
    "page"
  );
  await expect(page.getByText("vendor/.gitignore")).toBeVisible();

  const filesWidth = await page.locator(".files-panel").evaluate((element) => {
    return element.getBoundingClientRect().width;
  });
  const reviewWidth = await page.locator(".review-panel").evaluate((element) => {
    return element.getBoundingClientRect().width;
  });
  expect(filesWidth).toBe(240);
  expect(reviewWidth).toBe(360);
  await expect(page.locator(".files-content")).toHaveCSS("overflow-y", "auto");
  await expect(page.locator(".document-panel")).toHaveCSS("overflow-y", "auto");
  await expect(page.locator(".review-panel")).toHaveCSS("overflow-y", "auto");

  const docsDirectory = page.getByRole("button", { name: "docs", exact: true });
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("button", { name: /guide\.md/u })).toHaveCount(0);
  await docsDirectory.click();
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: /guide\.md/u })).toBeVisible();
  await docsDirectory.click();
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "false");

  const filter = page.getByRole("searchbox", { name: "Filter filenames" });
  await filter.fill("GUIDE");
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: /guide\.md/u })).toBeVisible();
  await expect(page.getByRole("button", { name: /README\.md/u })).toHaveCount(0);
  await filter.clear();
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "false");

  const internalLink = page.getByRole("link", { name: "Open guide" });
  await internalLink.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "Guide" })).toBeVisible();
  await expect(page.getByRole("button", { name: /guide\.md/u })).toHaveAttribute(
    "aria-current",
    "page"
  );
  await expect(docsDirectory).toHaveAttribute("aria-expanded", "true");

  await page.getByRole("link", { name: "Back to readme" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Workspace" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Jump to overview" })).toHaveAttribute(
    "href",
    "#overview"
  );
  await expect(page.locator("#overview")).toBeVisible();
  await expect(page.getByRole("link", { name: "External help" })).toHaveAttribute(
    "target",
    "_blank"
  );
  await expect(page.getByRole("link", { name: "External help" })).toHaveAttribute(
    "rel",
    "noopener noreferrer"
  );
  await expect(page.getByText("Unsafe action")).not.toHaveAttribute("href");
  await expect(page.getByText("Missing document")).not.toHaveAttribute("href");

  expect(requests.length).toBeGreaterThanOrEqual(4);
  expect(requests.every(({ authorization }) => authorization === undefined)).toBe(true);
  expect(await page.evaluate(() => localStorage.length)).toBe(0);
  expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
});

test("shows bounded document read failures and skips oversized reads", async ({ page }) => {
  const requests = await mockApi(page, fullWorkspace());
  await page.goto(`/`);
  await expect(page.getByRole("heading", { level: 1, name: "Workspace" })).toBeVisible();

  await page.getByRole("button", { name: /large\.md/u }).click();
  await expect(page.getByRole("heading", { name: "This document is too large" })).toBeVisible();
  expect(
    requests.some(({ url }) => new URL(url).searchParams.get("path") === "docs/large.md")
  ).toBe(false);

  await page.getByRole("button", { name: /invalid\.md/u }).click();
  await expect(
    page.getByRole("heading", { name: "This document is not valid UTF-8" })
  ).toBeVisible();

  await page.getByRole("button", { name: /deleted\.md/u }).click();
  await expect(
    page.getByRole("heading", { name: "This document is no longer available" })
  ).toBeVisible();

  await page.getByRole("button", { name: /unavailable\.md/u }).click();
  await expect(
    page.getByRole("heading", { name: "This document could not be opened" })
  ).toBeVisible();
});

test("renders hostile Markdown without executing HTML or requesting image sources", async ({
  page
}) => {
  const remoteSource = "https://images.invalid/remote.png?private=one";
  const rawSource = "http://images.invalid/raw.png?private=two";
  const dataSource = "data:image/png;base64,PRIVATE_THREE";
  const localSource = "./local.png?private=four";
  const imageSources = [remoteSource, rawSource, dataSource, localSource];
  const workspace: MockWorkspace = {
    state: {
      status: "changed",
      workspaceRevision: 1,
      documentCount: 1,
      initialDocumentPath: "README.md",
      navigation: [
        {
          kind: "document",
          name: "README.md",
          path: "README.md",
          sizeBytes: 500,
          availability: "ready",
          documentMetadataRevision: revision,
          reviewMetadataRevision: null
        }
      ],
      warnings: []
    },
    documents: new Map([
      [
        "README.md",
        {
          status: 200,
          source: `# Hostile

<script>document.body.dataset.executed = "yes"</script>

<span onclick="document.body.dataset.executed = 'event'">Safe raw text</span>

![Remote alt](${remoteSource})

<img alt="Raw alt" src="${rawSource}">

![Data alt](${dataSource})

![Local alt](${localSource})
`
        }
      ]
    ])
  };
  const networkURLs: string[] = [];
  page.on("request", (request) => {
    networkURLs.push(request.url());
  });
  await mockApi(page, workspace);
  await page.goto(`/`);

  await expect(page.getByRole("heading", { level: 1, name: "Hostile" })).toBeVisible();
  await expect(page.getByText("Safe raw text")).toBeVisible();
  await expect(page.locator(".markdown-body script")).toHaveCount(0);
  await expect(page.locator(".markdown-body img")).toHaveCount(0);
  await expect(page.locator(".markdown-body picture")).toHaveCount(0);
  await expect(page.locator(".markdown-media-placeholder")).toHaveCount(4);
  expect(await page.locator("body").getAttribute("data-executed")).toBeNull();

  const documentHtml = await page
    .locator(".markdown-body")
    .evaluate((element) => element.innerHTML);
  for (const source of imageSources) {
    expect(documentHtml).not.toContain(source);
    expect(networkURLs.some((url) => url.includes(source))).toBe(false);
  }
  expect(networkURLs.some((url) => url.includes("images.invalid"))).toBe(false);
});

test("reloads successfully from the normal workspace URL", async ({ page }) => {
  await mockApi(page, fullWorkspace());
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1, name: "Workspace" })).toBeVisible();
  await page.reload();
  await expect(page).toHaveURL("http://127.0.0.1:4173/");
  await expect(page.getByRole("heading", { level: 1, name: "Workspace" })).toBeVisible();
});

test("shows the empty workspace state without requesting a document", async ({ page }) => {
  const requests = await mockApi(page, {
    state: {
      status: "changed",
      workspaceRevision: 1,
      documentCount: 0,
      initialDocumentPath: null,
      navigation: [],
      warnings: []
    },
    documents: new Map()
  });

  await page.goto(`/`);

  await expect(page.getByText("No Markdown files were found in this workspace.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "No document selected" })).toBeVisible();
  expect(requests.map(({ url }) => new URL(url).pathname)).toEqual(["/api/state"]);
});
