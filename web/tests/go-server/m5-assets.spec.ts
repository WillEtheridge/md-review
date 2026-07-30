import { Buffer } from "node:buffer";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { expect, test, type Page, type Response, type TestInfo } from "@playwright/test";

const imageBytes = {
  png: Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACAQMAAABIeJ9nAAAAIGNIUk0AAHomAACAhAAA+gAAAIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAAGUExURU18/v///0yE6jUAAAABYktHRAH/Ai3eAAAAB3RJTUUH6gcdAh8Iq12T1AAAACV0RVh0ZGF0ZTpjcmVhdGUAMjAyNi0wNy0yOVQwMjozMTowOCswMDowMBiH3zMAAAAldEVYdGRhdGU6bW9kaWZ5ADIwMjYtMDctMjlUMDI6MzE6MDgrMDA6MDBp2mePAAAAKHRFWHRkYXRlOnRpbWVzdGFtcAAyMDI2LTA3LTI5VDAyOjMxOjA4KzAwOjAwPs9GUAAAAAxJREFUCNdjYGBgAAAABAABJzQnCgAAAABJRU5ErkJggg==",
    "base64"
  ),
  jpeg: Buffer.from(
    "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAMCAgICAgMCAgIDAwMDBAYEBAQEBAgGBgUGCQgKCgkICQkKDA8MCgsOCwkJDRENDg8QEBEQCgwSExIQEw8QEBD/2wBDAQMDAwQDBAgEBAgQCwkLEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBD/wAARCAACAAIDAREAAhEBAxEB/8QAFAABAAAAAAAAAAAAAAAAAAAABP/EABQQAQAAAAAAAAAAAAAAAAAAAAD/xAAVAQEBAAAAAAAAAAAAAAAAAAAHCP/EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAMAwEAAhEDEQA/ADrDEj//2Q==",
    "base64"
  ),
  gif: Buffer.from("R0lGODlhAgACAPAAAE18/gAAACH5BAAAAAAALAAAAAACAAIAAAIChFEAOw==", "base64"),
  webp: Buffer.from(
    "UklGRjoAAABXRUJQVlA4IC4AAACwAQCdASoCAAIAAgA0JaACdLoABQIAAP6XF/9cB/8tD/1aH8JD/gN18Wy+AAAA",
    "base64"
  )
} as const;

interface ServerEnvironment {
  baseURL: string;
  workspacePath: string;
}

interface AssetFixture {
  assetDirectoryName: string;
  assetDirectoryPath: string;
  documentName: string;
  documentPath: string;
}

interface AssetExchange {
  authorizationValid: boolean;
  contentType: string | undefined;
  errorCode: string | null;
  headers: Record<string, string>;
  reference: string | null;
  status: number;
  url: string;
}

interface ExpectedImage {
  alt: string;
  contentType: string;
  reference: string;
}

const expectedImages: readonly ExpectedImage[] = [
  {
    alt: "Contained PNG",
    contentType: "image/png",
    reference: "pixel.png"
  },
  {
    alt: "Contained JPEG",
    contentType: "image/jpeg",
    reference: "pixel.jpg"
  },
  {
    alt: "Contained GIF",
    contentType: "image/gif",
    reference: "pixel.gif"
  },
  {
    alt: "Contained WebP",
    contentType: "image/webp",
    reference: "pixel.webp"
  }
];

function serverEnvironment(): ServerEnvironment {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  const workspacePath = process.env.MDREVIEW_GO_SERVER_WORKSPACE;
  if (!baseURL || !workspacePath) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL, workspacePath };
}

function errorCode(value: unknown): string | null {
  if (typeof value !== "object" || value === null || !("error" in value)) {
    return null;
  }
  const error = value.error;
  if (typeof error !== "object" || error === null || !("code" in error)) {
    return null;
  }
  return typeof error.code === "string" ? error.code : null;
}

async function createAssetFixture(testInfo: TestInfo): Promise<AssetFixture> {
  const { workspacePath } = serverEnvironment();
  const projectSuffix = testInfo.project.name.replace(/[^a-z0-9]+/giu, "-").toLowerCase();
  const documentName = `m5-assets-${projectSuffix}.md`;
  const assetDirectoryName = `m5-assets-${projectSuffix}`;
  const documentPath = join(workspacePath, documentName);
  const assetDirectoryPath = join(workspacePath, assetDirectoryName);

  await mkdir(assetDirectoryPath);
  await Promise.all([
    writeFile(join(assetDirectoryPath, "pixel.png"), imageBytes.png),
    writeFile(join(assetDirectoryPath, "pixel.jpg"), imageBytes.jpeg),
    writeFile(join(assetDirectoryPath, "pixel.gif"), imageBytes.gif),
    writeFile(join(assetDirectoryPath, "pixel.webp"), imageBytes.webp),
    writeFile(
      join(assetDirectoryPath, "active.svg"),
      '<svg xmlns="http://www.w3.org/2000/svg" onload="globalThis.mdreviewSvgExecuted=true"><script>globalThis.mdreviewSvgExecuted=true</script></svg>'
    )
  ]);

  const source = [
    "# Contained image contract",
    "",
    `![Contained PNG](${assetDirectoryName}/pixel.png)`,
    `![Contained JPEG](${assetDirectoryName}/pixel.jpg)`,
    `![Contained GIF](${assetDirectoryName}/pixel.gif)`,
    `![Contained WebP](${assetDirectoryName}/pixel.webp)`,
    "",
    "![Remote image](https://images.invalid/secret.png)",
    "![Escaping image](../../outside-secret.png)",
    `![Unsupported SVG](${assetDirectoryName}/active.svg)`,
    `![Missing image](${assetDirectoryName}/missing.png)`,
    `![Query image](${assetDirectoryName}/pixel.png?token=source-secret)`,
    "",
    `<img src="${assetDirectoryName}/pixel.png" alt="Raw HTML image">`,
    ""
  ].join("\n");
  await writeFile(documentPath, source);

  return {
    assetDirectoryName,
    assetDirectoryPath,
    documentName,
    documentPath
  };
}

async function removeAssetFixture(fixture: AssetFixture): Promise<void> {
  await rm(fixture.documentPath, { force: true });
  await rm(fixture.assetDirectoryPath, { force: true, recursive: true });
}

async function openWorkspace(page: Page): Promise<Response> {
  const { baseURL } = serverEnvironment();
  const response = await page.goto(`${baseURL}/`);
  if (!response) {
    throw new Error("compiled mdReview server returned no application response");
  }
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
  return response;
}

function imageDirective(contentSecurityPolicy: string): string | undefined {
  return contentSecurityPolicy
    .split(";")
    .map((directive) => directive.trim())
    .find((directive) => directive.startsWith("img-src "));
}

async function decodedImageSources(page: Page): Promise<string[]> {
  const urls: string[] = [];
  for (const image of expectedImages) {
    const locator = page.getByRole("img", { name: image.alt, exact: true });
    await expect(locator).toHaveAttribute("loading", "lazy");
    await expect(locator).toHaveAttribute("decoding", "async");
    await expect(locator).toHaveAttribute("src", /^\/api\/asset\?documentPath=.+&reference=.+/u);
    await expect
      .poll(() =>
        locator.evaluate((element) => {
          if (!(element instanceof HTMLImageElement)) {
            return false;
          }
          return element.complete && element.naturalWidth > 0 && element.naturalHeight > 0;
        })
      )
      .toBe(true);
    const source = await locator.getAttribute("src");
    if (!source) {
      throw new Error(`loaded image ${image.alt} has no source`);
    }
    urls.push(source);
  }
  return urls;
}

test("compiled binary keeps native lazy images contained and inert", async ({ page }, testInfo) => {
  const environment = serverEnvironment();
  const fixture = await createAssetFixture(testInfo);
  const requestURLs: string[] = [];
  const assetExchanges: Array<Promise<AssetExchange>> = [];

  page.on("request", (request) => {
    requestURLs.push(request.url());
  });
  page.on("response", (response) => {
    const responseURL = new URL(response.url());
    if (responseURL.pathname !== "/api/asset") {
      return;
    }
    assetExchanges.push(
      (async () => {
        const [headers, requestHeaders] = await Promise.all([
          response.allHeaders(),
          response.request().allHeaders()
        ]);
        const body: unknown = response.ok() ? null : await response.json();
        return {
          authorizationValid: requestHeaders.authorization === undefined,
          contentType: headers["content-type"],
          errorCode: errorCode(body),
          headers,
          reference: responseURL.searchParams.get("reference"),
          status: response.status(),
          url: response.url()
        };
      })()
    );
  });

  try {
    const shellResponse = await openWorkspace(page);
    const shellHeaders = await shellResponse.allHeaders();
    expect(imageDirective(shellHeaders["content-security-policy"] ?? "")).toBe("img-src 'self'");
    expect(shellHeaders["referrer-policy"]).toBe("no-referrer");
    expect(shellHeaders["x-content-type-options"]).toBe("nosniff");

    const fixtureButton = page.getByRole("button", {
      name: fixture.documentName,
      exact: true
    });
    await expect(fixtureButton).toBeVisible({ timeout: 8_000 });
    await fixtureButton.click();
    await expect(
      page.getByRole("heading", { level: 1, name: "Contained image contract" })
    ).toBeVisible();

    const firstSources = await decodedImageSources(page);
    await expect(page.locator(".markdown-body img")).toHaveCount(expectedImages.length);
    expect(firstSources.every((source) => source.startsWith("/api/asset?"))).toBe(true);

    await expect(page.getByRole("img", { name: "Image: Remote image", exact: true })).toBeVisible();
    await expect(page.getByRole("img", { name: "Image: Query image", exact: true })).toBeVisible();
    await expect(
      page.getByRole("img", { name: "Image: Raw HTML image", exact: true })
    ).toBeVisible();
    await expect(
      page.getByRole("img", { name: "Image: Escaping image", exact: true })
    ).toContainText("Image could not be loaded.");
    await expect(
      page.getByRole("img", { name: "Image: Missing image", exact: true })
    ).toContainText("Image could not be loaded.");
    await expect(
      page.getByRole("img", { name: "Image: Unsupported SVG", exact: true })
    ).toContainText("Image could not be loaded.");

    expect(requestURLs.some((url) => url.includes("images.invalid"))).toBe(false);

    const firstExchanges = (await Promise.all(assetExchanges)).filter((exchange) => {
      return new URL(exchange.url).searchParams.get("documentPath") === fixture.documentName;
    });
    expect(firstExchanges).toHaveLength(7);
    expect(firstExchanges.every((exchange) => exchange.authorizationValid)).toBe(true);
    expect(
      firstExchanges.every((exchange) => new URL(exchange.url).origin === environment.baseURL)
    ).toBe(true);

    const exchangesByReference = new Map(
      firstExchanges.map((exchange) => [exchange.reference, exchange])
    );
    for (const expectedImage of expectedImages) {
      const exchange = exchangesByReference.get(
        `${fixture.assetDirectoryName}/${expectedImage.reference}`
      );
      expect(exchange?.status).toBe(200);
      expect(exchange?.contentType).toBe(expectedImage.contentType);
      expect(exchange?.headers["cache-control"]).toBe("no-store");
      expect(exchange?.headers["x-content-type-options"]).toBe("nosniff");
      expect(exchange?.headers.etag).toBeUndefined();
      expect(exchange?.headers["last-modified"]).toBeUndefined();
      expect(exchange?.headers["accept-ranges"]).toBeUndefined();
      expect(exchange?.headers["content-encoding"]).toBeUndefined();
      expect(imageDirective(exchange?.headers["content-security-policy"] ?? "")).toBe(
        "img-src 'self'"
      );
    }

    const escaping = exchangesByReference.get("../../outside-secret.png");
    expect(escaping?.status).toBe(404);
    expect(escaping?.errorCode).toBe("assetNotFound");
    const missing = exchangesByReference.get(`${fixture.assetDirectoryName}/missing.png`);
    expect(missing?.status).toBe(404);
    expect(missing?.errorCode).toBe("assetNotFound");
    const unsupported = exchangesByReference.get(`${fixture.assetDirectoryName}/active.svg`);
    expect(unsupported?.status).toBe(415);
    expect(unsupported?.errorCode).toBe("assetUnsupportedType");

    await page.getByRole("button", { name: "README.md", exact: true }).click();
    await expect(
      page.getByRole("heading", { level: 1, name: "Integration workspace" })
    ).toBeVisible();

    await fixtureButton.click();
    await expect(
      page.getByRole("heading", { level: 1, name: "Contained image contract" })
    ).toBeVisible();
    const secondSources = await decodedImageSources(page);
    expect(secondSources).toEqual(firstSources);
  } finally {
    await removeAssetFixture(fixture);
  }
});
