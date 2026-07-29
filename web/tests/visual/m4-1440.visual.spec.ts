import { expect, test } from "@playwright/test";

import { discussionState, openVisualState, prepareScreenshot } from "./support";

for (const theme of ["light", "dark"] as const) {
  test(`1440 ${theme} rich document and active discussion`, async ({ page }) => {
    const session = await openVisualState(page, theme, discussionState());
    const activeThread = page.locator('article[data-thread-id="thread_evidence"]');
    await activeThread.locator(".thread-target").click();
    await expect(activeThread).toHaveAttribute("data-active", "true");
    await expect(
      page.locator('.review-highlight.is-active[data-thread-id="thread_evidence"]')
    ).toBeVisible();

    await prepareScreenshot(page);
    await expect(page).toHaveScreenshot(`rich-discussion-${theme}.png`);
    expect(session.externalRequests).toEqual([]);
  });
}
