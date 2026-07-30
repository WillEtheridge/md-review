import { copyFile, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, test, type Page } from "@playwright/test";

interface PersistedMessage {
  author: {
    name: string;
    type: "agent" | "human";
  };
  body: string;
}

interface PersistedThread {
  id: string;
  messages: PersistedMessage[];
  status: "handled" | "open" | "resolved";
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

async function openWorkspace(page: Page): Promise<void> {
  const { baseURL } = serverEnvironment();
  await page.goto(`${baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
}

test("m7 compiled browser loads the accepted agent result and records human resolution", async ({
  page
}, testInfo) => {
  const environment = serverEnvironment();
  const resultDirectory = fileURLToPath(
    new URL("../../../testdata/integration/m6-skill-result/", import.meta.url)
  );
  const evaluation = JSON.parse(
    await readFile(join(resultDirectory, "evaluation.json"), "utf8")
  ) as {
    agent: string;
    modelFamily: string;
    schemaVersion: number;
  };
  expect(evaluation).toMatchObject({
    agent: "m6_skill_forward_eval",
    modelFamily: "Terra",
    schemaVersion: 1
  });

  const documentPath = `m7-agent-handoff-${testInfo.project.name}.md`;
  const destinationDocument = join(environment.workspace, documentPath);
  const destinationSidecar = `${destinationDocument}.review.json`;
  const sourceDocument = join(resultDirectory, "launch-plan.md");
  const sourceSidecar = join(resultDirectory, "launch-plan.md.review.json");
  const acceptedMarkdown = await readFile(sourceDocument, "utf8");
  const acceptedSidecar = await readFile(sourceSidecar, "utf8");

  try {
    await copyFile(sourceDocument, destinationDocument);
    await copyFile(sourceSidecar, destinationSidecar);
    await openWorkspace(page);
    const documentButton = page.getByRole("button", {
      name: new RegExp(documentPath.replaceAll(".", "\\."), "u")
    });
    await expect(documentButton).toBeVisible({ timeout: 10_000 });
    await documentButton.click();
    await expect(page.getByRole("heading", { level: 1, name: "Launch plan" })).toBeVisible();
    await expect(
      page.getByText("Contact support@example.com for release questions.")
    ).toBeVisible();

    await page.getByRole("checkbox", { name: "Resolved" }).check();
    const handled = page.locator('.thread-card[data-thread-id="thread-contact"]');
    const awaitingHuman = page.locator('.thread-card[data-thread-id="thread-date"]');
    const history = page.locator('.thread-card[data-thread-id="thread-title-history"]');
    await expect(handled.locator(".thread-metadata")).toContainText("Handled");
    await expect(
      handled.getByText("Updated the release contact to support@example.com.")
    ).toBeVisible();
    await expect(awaitingHuman.locator(".thread-metadata")).toContainText("Open");
    await expect(
      awaitingHuman.getByText(
        "Do not choose a release date yet; I need to provide it in a follow-up."
      )
    ).toBeVisible();
    await expect(history.locator(".thread-metadata")).toContainText("Resolved");

    await handled.getByRole("button", { name: /Resolve/u }).click();
    await expect(handled.locator(".thread-metadata")).toContainText("Resolved");

    expect(await readFile(destinationDocument, "utf8")).toBe(acceptedMarkdown);
    const persistedText = await readFile(destinationSidecar, "utf8");
    const persisted = JSON.parse(persistedText) as {
      schemaVersion: number;
      threads: PersistedThread[];
    };
    const contact = persisted.threads.find((thread) => thread.id === "thread-contact");
    const date = persisted.threads.find((thread) => thread.id === "thread-date");
    const titleHistory = persisted.threads.find((thread) => thread.id === "thread-title-history");
    expect(persisted.schemaVersion).toBe(1);
    expect(contact?.status).toBe("resolved");
    expect(contact?.messages[1]).toMatchObject({
      author: {
        name: "m6_skill_forward_eval",
        type: "agent"
      },
      body: "Updated the release contact to support@example.com."
    });
    expect(date?.status).toBe("open");
    expect(titleHistory?.status).toBe("resolved");
    expect(acceptedSidecar).toContain('"status": "handled"');
  } finally {
    await rm(destinationDocument, { force: true });
    await rm(destinationSidecar, { force: true });
  }
});
