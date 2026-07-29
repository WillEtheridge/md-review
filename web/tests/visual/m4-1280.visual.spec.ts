import { expect, test } from "@playwright/test";

import { conflictState, detachedState, openVisualState, prepareScreenshot } from "./support";

test("1280 light detached review state", async ({ page }) => {
  const session = await openVisualState(page, "light", detachedState());
  const detachedThread = page.locator('[data-thread-id="thread_detached"]');
  await expect(detachedThread).toHaveAttribute("data-attachment", "detached");
  await detachedThread.locator(".thread-target").click();
  await expect(detachedThread).toHaveAttribute("data-active", "true");

  await prepareScreenshot(page);
  await expect(page).toHaveScreenshot("detached-review-light.png");
  expect(session.externalRequests).toEqual([]);
});

test("1280 dark conflict composer state", async ({ page }) => {
  const session = await openVisualState(page, "dark", conflictState());
  const conflictThread = page.locator('[data-thread-id="thread_conflict"]');
  await conflictThread.getByRole("button", { name: "Reply to Document comment" }).click();
  const reply = conflictThread.getByRole("textbox", { name: "Reply" });
  await reply.fill(
    "I have kept this reply draft while checking the newer sidecar revision before retrying."
  );
  await reply.press("Control+Enter");
  await expect(conflictThread.getByRole("alert")).toContainText("changed on disk");
  await expect(reply).toHaveValue(
    "I have kept this reply draft while checking the newer sidecar revision before retrying."
  );

  await prepareScreenshot(page);
  await expect(page).toHaveScreenshot("conflict-composer-dark.png");
  expect(session.externalRequests).toEqual([]);
});
