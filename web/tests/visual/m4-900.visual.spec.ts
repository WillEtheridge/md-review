import { expect, test } from "@playwright/test";

import {
  documentErrorState,
  emptyReviewState,
  openVisualState,
  prepareScreenshot
} from "./support";

interface NarrowGeometry {
  viewport: number;
  page: number;
  shell: number;
  files: number;
  document: number;
  review: number;
}

async function narrowGeometry(page: import("@playwright/test").Page): Promise<NarrowGeometry> {
  return page.evaluate(() => {
    const shell = document.querySelector<HTMLElement>(".app-shell");
    const files = document.querySelector<HTMLElement>(".files-panel");
    const documentPanel = document.querySelector<HTMLElement>(".document-panel");
    const review = document.querySelector<HTMLElement>(".review-panel");
    if (!shell || !files || !documentPanel || !review) {
      throw new Error("fixed layout panels are missing");
    }
    return {
      viewport: window.innerWidth,
      page: document.documentElement.scrollWidth,
      shell: shell.getBoundingClientRect().width,
      files: files.getBoundingClientRect().width,
      document: documentPanel.getBoundingClientRect().width,
      review: review.getBoundingClientRect().width
    };
  });
}

function expectFrozenNarrowLayout(geometry: NarrowGeometry): void {
  expect(geometry).toEqual({
    viewport: 900,
    page: 1000,
    shell: 1000,
    files: 240,
    document: 400,
    review: 360
  });
}

test("900 light empty review and intentional page overflow", async ({ page }) => {
  const session = await openVisualState(page, "light", emptyReviewState());
  expectFrozenNarrowLayout(await narrowGeometry(page));

  await prepareScreenshot(page);
  await expect(page).toHaveScreenshot("empty-review-overflow-light.png");
  expect(session.externalRequests).toEqual([]);
});

test("900 dark document error state", async ({ page }) => {
  const session = await openVisualState(page, "dark", documentErrorState());
  await expect(
    page.getByRole("heading", {
      name: "This document is not valid UTF-8"
    })
  ).toBeVisible();
  expectFrozenNarrowLayout(await narrowGeometry(page));

  await prepareScreenshot(page);
  await expect(page).toHaveScreenshot("document-error-dark.png");
  expect(session.externalRequests).toEqual([]);
});
