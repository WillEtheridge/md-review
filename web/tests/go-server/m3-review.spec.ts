import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { expect, test, type Page } from "@playwright/test";

interface PersistedMessage {
  author: {
    type: "human" | "agent";
    name: string;
  };
  body: string;
  editedAt?: string;
}

interface PersistedThread {
  id: string;
  status: "open" | "handled" | "resolved";
  messages: PersistedMessage[];
}

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
  await page.goto("about:blank");
  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
}

async function pressButton(
  button: ReturnType<Page["getByRole"]>,
  key: "Enter" | "Space" = "Enter"
): Promise<void> {
  await button.focus();
  await expect(button).toBeFocused();
  await button.press(key);
}

test("compiled browser completes the keyboard review lifecycle and persists it", async ({
  page
}, testInfo) => {
  const { workspace } = serverEnvironment();
  const label = projectLabel(testInfo.project.name);
  const documentPath = `m3-ui-${testInfo.project.name}.md`;
  const sidecarPath = join(workspace, `${documentPath}.review.json`);

  await openWorkspace(page);
  await page
    .getByRole("button", { name: new RegExp(documentPath.replace(".", "\\."), "u") })
    .click();
  await expect(
    page.getByRole("heading", { level: 1, name: `${label} browser review workflow` })
  ).toBeVisible();

  const workflow = page.locator('.thread-card[data-thread-id="thread_ui_workflow"]');
  const deletable = page.locator('.thread-card[data-thread-id="thread_ui_delete"]');
  const agent = page.locator('.thread-card[data-thread-id="thread_ui_agent"]');
  await expect(workflow.locator(".thread-metadata")).toContainText("Handled");
  await expect(agent.getByText("Agent-authored browser explanation.")).toBeVisible();
  await expect(workflow.locator(".message-author")).toContainText("edited");

  await page.getByRole("checkbox", { name: "Resolved" }).check();

  await pressButton(workflow.getByRole("button", { name: "Reply" }));
  const reply = workflow.getByRole("textbox", { name: "Reply" });
  await reply.fill("Keyboard reply through the compiled browser.");
  await reply.press("Control+Enter");
  await expect(workflow.getByText("Keyboard reply through the compiled browser.")).toBeVisible();
  await expect(workflow.locator(".thread-metadata")).toContainText("Open");

  await pressButton(workflow.getByRole("button", { name: "Resolve" }), "Space");
  await expect(workflow.locator(".thread-metadata")).toContainText("Resolved");
  await expect(workflow.locator(".thread-target")).toBeFocused();
  await pressButton(
    workflow.locator(".thread-card-header").getByRole("button", { name: /More actions for/u })
  );
  await pressButton(workflow.getByRole("button", { name: "Reopen" }));
  await expect(workflow.locator(".thread-metadata")).toContainText("Open");
  await expect(workflow.locator(".thread-target")).toBeFocused();

  await pressButton(
    deletable.locator(".thread-card-header").getByRole("button", { name: /More actions for/u })
  );
  await pressButton(deletable.getByRole("button", { name: "Delete thread" }));
  await pressButton(deletable.getByRole("button", { name: "Cancel" }));
  await expect(
    deletable.locator(".thread-card-header").getByRole("button", { name: /More actions for/u })
  ).toBeFocused();
  await pressButton(
    deletable.locator(".thread-card-header").getByRole("button", { name: /More actions for/u })
  );
  await pressButton(deletable.getByRole("button", { name: "Delete thread" }));
  await pressButton(deletable.getByRole("button", { name: "Delete thread" }));
  await expect(deletable).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Comment on document" })).toBeFocused();

  await pressButton(agent.getByRole("button", { name: "Resolve" }));
  await expect(agent.locator(".thread-metadata")).toContainText("Resolved");

  const sidecarText = await readFile(sidecarPath, "utf8");
  const sidecar = JSON.parse(sidecarText) as {
    schemaVersion: number;
    threads: PersistedThread[];
  };
  const persistedWorkflow = sidecar.threads.find((thread) => thread.id === "thread_ui_workflow");
  const persistedAgent = sidecar.threads.find((thread) => thread.id === "thread_ui_agent");

  expect(sidecar.schemaVersion).toBe(1);
  expect(sidecar.threads.some((thread) => thread.id === "thread_ui_delete")).toBe(false);
  expect(persistedWorkflow?.status).toBe("open");
  expect(persistedWorkflow?.messages).toHaveLength(2);
  expect(persistedWorkflow?.messages[0]).toMatchObject({
    author: {
      type: "human",
      name: "Reviewer"
    },
    body: "Original browser feedback."
  });
  expect(persistedWorkflow?.messages[0]?.editedAt).toBe("2026-07-28T08:05:00Z");
  expect(persistedWorkflow?.messages[1]).toMatchObject({
    author: {
      type: "human",
      name: "Reviewer"
    },
    body: "Keyboard reply through the compiled browser."
  });
  expect(persistedAgent?.status).toBe("resolved");
  expect(persistedAgent?.messages[1]).toMatchObject({
    author: {
      type: "agent",
      name: "Codex"
    },
    body: "Agent-authored browser explanation."
  });
});
