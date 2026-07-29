import { expect, test, type Page } from "@playwright/test";

interface FontManifest {
  families: Array<{
    family: string;
    license: {
      path: string;
    };
    files: Array<{
      path: string;
    }>;
  }>;
}

function serverEnvironment(): {
  baseURL: string;
} {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  if (!baseURL) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL };
}

async function openWorkspace(page: Page): Promise<void> {
  const { baseURL } = serverEnvironment();
  await page.goto("about:blank");
  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
}

test("compiled binary serves the complete offline typography and remembered themes", async ({
  page
}) => {
  const { baseURL } = serverEnvironment();
  const requests: string[] = [];
  page.on("request", (request) => {
    requests.push(request.url());
  });

  await openWorkspace(page);
  await page.getByRole("button", { name: /m4-fonts\.md/u }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Offline typography" })).toBeVisible();
  await page.evaluate(async () => {
    await document.fonts.ready;
    await Promise.all([
      document.fonts.load('400 16px "Inter"', "Interface"),
      document.fonts.load('400 16px "PT Serif"', "Reading"),
      document.fonts.load('400 16px "JetBrains Mono"', "const")
    ]);
  });

  const families = await page.evaluate(() => {
    const reading = document.querySelector(".markdown-body");
    const code = document.querySelector(".markdown-body code");
    if (!reading || !code) {
      throw new Error("typography fixture did not render");
    }
    return {
      interface: getComputedStyle(document.body).fontFamily,
      reading: getComputedStyle(reading).fontFamily,
      code: getComputedStyle(code).fontFamily,
      loaded: [
        document.fonts.check('400 16px "Inter"', "Interface"),
        document.fonts.check('400 16px "PT Serif"', "Reading"),
        document.fonts.check('400 16px "JetBrains Mono"', "const")
      ]
    };
  });
  expect(families.interface).toContain("Inter");
  expect(families.reading).toContain("PT Serif");
  expect(families.code).toContain("JetBrains Mono");
  expect(families.loaded).toEqual([true, true, true]);

  const manifest = await page.evaluate(async () => {
    const response = await fetch("/fonts/manifest.json");
    if (!response.ok) {
      throw new Error(`font manifest returned ${String(response.status)}`);
    }
    return (await response.json()) as FontManifest;
  });
  expect(manifest.families.map(({ family }) => family).sort()).toEqual([
    "Inter",
    "JetBrains Mono",
    "PT Serif"
  ]);
  const assetPaths = manifest.families.flatMap((family) => [
    family.license.path,
    ...family.files.map(({ path }) => path)
  ]);
  const assetResults = await page.evaluate(async (paths) => {
    return Promise.all(
      paths.map(async (path) => {
        const response = await fetch(`/fonts/${path}`);
        return {
          path,
          status: response.status,
          size: (await response.arrayBuffer()).byteLength
        };
      })
    );
  }, assetPaths);
  expect(assetResults.every(({ status, size }) => status === 200 && size > 0)).toBe(true);

  const root = page.locator("html");
  await expect(root).toHaveAttribute("data-theme", "system");
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(root).toHaveCSS("color-scheme", "dark");
  await expect(page.getByRole("button", { name: "Dark" })).toHaveAttribute("aria-pressed", "true");
  const darkSurface = await page.locator("body").evaluate((element) => {
    return getComputedStyle(element).backgroundColor;
  });
  await page.emulateMedia({ colorScheme: "light" });
  await expect
    .poll(() =>
      page.locator("body").evaluate((element) => {
        return getComputedStyle(element).backgroundColor;
      })
    )
    .not.toBe(darkSurface);
  await expect(root).toHaveAttribute("data-theme", "system");
  await expect(page.getByRole("button", { name: "Light" })).toHaveAttribute("aria-pressed", "true");

  await page.getByRole("button", { name: "Dark" }).click();
  await expect(root).toHaveAttribute("data-theme", "dark");
  expect(await page.evaluate(() => localStorage.getItem("mdreview.theme"))).toBe("dark");

  await openWorkspace(page);
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.getByRole("button", { name: "Dark" })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Light" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  expect(await page.evaluate(() => localStorage.getItem("mdreview.theme"))).toBe("light");

  expect(
    requests.every((requestURL) => {
      const url = new URL(requestURL);
      return url.origin === baseURL;
    })
  ).toBe(true);
});
