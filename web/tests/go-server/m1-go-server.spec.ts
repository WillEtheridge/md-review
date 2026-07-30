import { expect, test } from "@playwright/test";

function serverEnvironment(): { baseURL: string } {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  if (!baseURL) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL };
}

test("compiled server navigates the contained fixture with same-origin requests", async ({
  page
}) => {
  const { baseURL } = serverEnvironment();
  const observedRequests: Array<{ url: string; authorization: string | undefined }> = [];
  page.on("request", (request) => {
    observedRequests.push({
      url: request.url(),
      authorization: request.headers()["authorization"]
    });
  });

  await page.goto(`${baseURL}/`);

  await expect(page).toHaveURL(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Files" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Comments", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /ignored\.md/u })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /escape\.md/u })).toHaveCount(0);

  await page.getByRole("link", { name: "Open the guide" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Guide" })).toBeVisible();
  await page.getByRole("link", { name: "Return to the README" }).click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();

  const apiRequests = observedRequests.filter(({ url }) =>
    new URL(url).pathname.startsWith("/api/")
  );
  expect(apiRequests.length).toBeGreaterThanOrEqual(3);
  expect(apiRequests.every(({ authorization }) => authorization === undefined)).toBe(true);

  await page.reload();
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
});

test("compiled server and CSP keep every Markdown image inert", async ({ page }) => {
  const { baseURL } = serverEnvironment();
  const requestURLs: string[] = [];
  page.on("request", (request) => {
    requestURLs.push(request.url());
  });

  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();

  await expect(page.locator(".markdown-media-placeholder")).toHaveCount(4);
  await expect(page.locator(".markdown-body img")).toHaveCount(0);
  await expect(page.locator(".markdown-body script")).toHaveCount(0);
  expect(await page.evaluate(() => Reflect.has(globalThis, "mdReviewHostileScriptExecuted"))).toBe(
    false
  );

  const documentHTML = await page
    .locator(".markdown-body")
    .evaluate((document) => document.innerHTML);
  for (const forbidden of [
    "images.invalid",
    "raw-images.invalid",
    "assets/private.png",
    "data:image/png"
  ]) {
    expect(documentHTML).not.toContain(forbidden);
    expect(requestURLs.some((requestURL) => requestURL.includes(forbidden))).toBe(false);
  }
});

test("compiled server exposes bounded read errors after a normal navigation", async ({ page }) => {
  const { baseURL } = serverEnvironment();
  const documentRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/document") {
      documentRequests.push(url.searchParams.get("path") ?? "");
    }
  });

  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();

  await page.getByRole("button", { name: /large\.md/u }).click();
  await expect(page.getByRole("heading", { name: "This document is too large" })).toBeVisible();
  expect(documentRequests).not.toContain("large.md");

  await page.getByRole("button", { name: /invalid\.md/u }).click();
  await expect(
    page.getByRole("heading", { name: "This document is not valid UTF-8" })
  ).toBeVisible();

  const normalPage = await page.context().newPage();
  await normalPage.goto(`${baseURL}/`);
  await expect(
    normalPage.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
  await normalPage.close();
});

test("compiled server enforces host, traversal, and security headers", async ({ request }) => {
  const { baseURL } = serverEnvironment();

  const shell = await request.get(`${baseURL}/`);
  expect(shell.status()).toBe(200);
  const contentSecurityPolicy = shell.headers()["content-security-policy"] ?? "";
  expect(contentSecurityPolicy).toContain("img-src 'self'");
  expect(contentSecurityPolicy).not.toContain("img-src blob:");
  expect(contentSecurityPolicy).not.toContain("img-src data:");
  expect(shell.headers()["referrer-policy"]).toBe("no-referrer");
  expect(shell.headers()["x-content-type-options"]).toBe("nosniff");

  expect((await request.get(`${baseURL}/api/state`)).status()).toBe(200);
  expect(
    (
      await request.get(`${baseURL}/api/state`, {
        headers: {
          Host: `localhost:${new URL(baseURL).port}`
        }
      })
    ).status()
  ).toBe(400);

  const traversal = await request.get(
    `${baseURL}/api/document?path=${encodeURIComponent("../outside-secret.md")}`
  );
  expect(traversal.status()).toBe(400);
  expect(await traversal.text()).not.toContain("outside secret");

  const state = await request.get(`${baseURL}/api/state`);
  expect(state.status()).toBe(200);
});
