import { rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";

import {
  expect,
  test,
  type APIRequestContext,
  type APIResponse,
  type BrowserContext,
  type TestInfo
} from "@playwright/test";

interface ServerEnvironment {
  baseURL: string;
  workspace: string;
}

interface NavigationEntry {
  kind: "directory" | "document";
  name: string;
  path: string;
  children?: NavigationEntry[];
  documentMetadataRevision?: string;
  reviewMetadataRevision?: string | null;
}

interface ChangedState {
  status: "changed";
  workspaceRevision: number;
  documentCount: number;
  initialDocumentPath: string | null;
  navigation: NavigationEntry[];
  warnings: unknown[];
}

interface UnchangedState {
  status: "unchanged";
  workspaceRevision: number;
}

type StateResponse = ChangedState | UnchangedState;

interface DocumentSnapshot {
  path: string;
  revision: string;
  source: string;
}

interface ReviewSnapshot {
  documentRevision: string;
  reviewRevision: string;
  threads: Array<{
    id: string;
    status: "open" | "handled" | "resolved";
    messages: Array<{
      id: string;
      body: string;
    }>;
  }>;
}

const metadataRevisionPattern = /^[0-9a-f]{64}$/u;

function serverEnvironment(): ServerEnvironment {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  const workspace = process.env.MDREVIEW_GO_SERVER_WORKSPACE;
  if (!baseURL || !workspace) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL, workspace };
}

function requestHeaders(): Record<string, string> {
  return {};
}

function projectSuffix(testInfo: TestInfo): string {
  return testInfo.project.name.replaceAll(/[^a-z0-9-]/giu, "-").toLowerCase();
}

function flattenDocuments(entries: NavigationEntry[]): NavigationEntry[] {
  return entries.flatMap((entry) =>
    entry.kind === "directory" ? flattenDocuments(entry.children ?? []) : [entry]
  );
}

function findDocument(state: ChangedState, path: string): NavigationEntry | undefined {
  return flattenDocuments(state.navigation).find((entry) => entry.path === path);
}

function hasMetadataRevisions(entry: NavigationEntry | undefined): boolean {
  return (
    metadataRevisionPattern.test(entry?.documentMetadataRevision ?? "") &&
    metadataRevisionPattern.test(entry?.reviewMetadataRevision ?? "")
  );
}

async function readState(
  request: APIRequestContext,
  environment: ServerEnvironment,
  since?: number
): Promise<StateResponse> {
  const query = since === undefined ? "" : `?since=${String(since)}`;
  const response = await request.get(`${environment.baseURL}/api/state${query}`, {
    headers: requestHeaders()
  });
  expect(response.status()).toBe(200);
  return (await response.json()) as StateResponse;
}

async function readFullState(
  request: APIRequestContext,
  environment: ServerEnvironment
): Promise<ChangedState> {
  const state = await readState(request, environment);
  expect(state.status).toBe("changed");
  if (state.status !== "changed") {
    throw new Error("bootstrap state response was not complete");
  }
  return state;
}

async function waitForChangedState(
  request: APIRequestContext,
  environment: ServerEnvironment,
  since: number,
  accepts: (state: ChangedState) => boolean,
  message: string
): Promise<ChangedState> {
  let accepted: ChangedState | undefined;
  await expect
    .poll(
      async () => {
        const state = await readState(request, environment, since);
        if (state.status === "changed" && accepts(state)) {
          accepted = state;
          return true;
        }
        return false;
      },
      {
        intervals: [0, 50, 100, 250, 500],
        message,
        timeout: 8_000
      }
    )
    .toBe(true);
  if (!accepted) {
    throw new Error(`poll completed without a changed state: ${message}`);
  }
  return accepted;
}

async function readDocument(
  request: APIRequestContext,
  environment: ServerEnvironment,
  path: string
): Promise<DocumentSnapshot> {
  const response = await request.get(
    `${environment.baseURL}/api/document?path=${encodeURIComponent(path)}`,
    {
      headers: requestHeaders()
    }
  );
  expect(response.status()).toBe(200);
  return (await response.json()) as DocumentSnapshot;
}

async function readReview(
  request: APIRequestContext,
  environment: ServerEnvironment,
  path: string
): Promise<ReviewSnapshot> {
  const response = await request.get(
    `${environment.baseURL}/api/review?path=${encodeURIComponent(path)}`,
    {
      headers: requestHeaders()
    }
  );
  expect(response.status()).toBe(200);
  return (await response.json()) as ReviewSnapshot;
}

async function expectError(response: APIResponse, status: number, code: string): Promise<void> {
  expect(response.status()).toBe(status);
  const body = (await response.json()) as {
    error?: {
      code?: string;
    };
  };
  expect(body.error?.code).toBe(code);
}

async function removePaths(paths: string[]): Promise<void> {
  await Promise.all(paths.map(async (path) => rm(path, { force: true })));
}

function reviewSidecar(threadID?: string, messageBody?: string): string {
  if (!threadID || !messageBody) {
    return `${JSON.stringify({ schemaVersion: 1, threads: [] }, null, 2)}\n`;
  }
  return `${JSON.stringify(
    {
      schemaVersion: 1,
      threads: [
        {
          id: threadID,
          anchor: {
            type: "document"
          },
          status: "open",
          messages: [
            {
              id: `message_${threadID}`,
              author: {
                type: "agent",
                name: "External agent"
              },
              body: messageBody,
              createdAt: "2026-07-29T12:00:00Z"
            }
          ]
        }
      ]
    },
    null,
    2
  )}\n`;
}

test("compiled server exposes strict conditional workspace state", async ({ request }) => {
  const environment = serverEnvironment();
  const state = await readFullState(request, environment);

  expect(state.workspaceRevision).toBeGreaterThan(0);
  expect(state.documentCount).toBe(flattenDocuments(state.navigation).length);
  for (const document of flattenDocuments(state.navigation)) {
    expect(document.documentMetadataRevision).toMatch(metadataRevisionPattern);
    expect(document).toHaveProperty("reviewMetadataRevision");
    if (document.reviewMetadataRevision !== null) {
      expect(document.reviewMetadataRevision).toMatch(metadataRevisionPattern);
    }
  }

  const unchangedResponse = await request.get(
    `${environment.baseURL}/api/state?since=${String(state.workspaceRevision)}`,
    {
      headers: requestHeaders()
    }
  );
  expect(unchangedResponse.status()).toBe(200);
  expect(await unchangedResponse.json()).toEqual({
    status: "unchanged",
    workspaceRevision: state.workspaceRevision
  });

  for (const query of [
    "since=0",
    "since=abc",
    "since=1&since=1",
    "since=1&extra=value",
    "since=18446744073709551616"
  ]) {
    await expectError(
      await request.get(`${environment.baseURL}/api/state?${query}`, {
        headers: requestHeaders()
      }),
      400,
      "invalidWorkspaceRevision"
    );
  }
});

test("external Markdown and sidecar changes publish as one navigable state", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const suffix = projectSuffix(testInfo);
  const originalPath = `m5-external-${suffix}.md`;
  const renamedPath = `m5-renamed-${suffix}.md`;
  const originalDocumentPath = join(environment.workspace, originalPath);
  const originalSidecarPath = `${originalDocumentPath}.review.json`;
  const renamedDocumentPath = join(environment.workspace, renamedPath);
  const renamedSidecarPath = `${renamedDocumentPath}.review.json`;
  const temporaryDocumentPath = `${originalDocumentPath}.replacement`;
  const temporarySidecarPath = `${originalSidecarPath}.replacement`;
  const cleanupPaths = [
    originalDocumentPath,
    originalSidecarPath,
    renamedDocumentPath,
    renamedSidecarPath,
    temporaryDocumentPath,
    temporarySidecarPath
  ];

  await removePaths(cleanupPaths);
  try {
    const baseline = await readFullState(request, environment);
    const initialSource = "# External document\n\nInitial source.\n";
    await writeFile(originalDocumentPath, initialSource, "utf8");
    await writeFile(originalSidecarPath, reviewSidecar(), "utf8");

    const createdState = await waitForChangedState(
      request,
      environment,
      baseline.workspaceRevision,
      (state) => {
        const entry = findDocument(state, originalPath);
        return hasMetadataRevisions(entry);
      },
      "created Markdown and adjacent sidecar should appear together"
    );
    const createdEntry = findDocument(createdState, originalPath);
    expect(createdEntry?.documentMetadataRevision).toMatch(metadataRevisionPattern);
    expect(createdEntry?.reviewMetadataRevision).toMatch(metadataRevisionPattern);

    const initialDocument = await readDocument(request, environment, originalPath);
    const initialReview = await readReview(request, environment, originalPath);
    expect(initialDocument.source).toBe(initialSource);
    expect(initialReview.documentRevision).toBe(initialDocument.revision);
    expect(initialReview.threads).toEqual([]);

    const updatedSource = "# External document\n\nUpdated source from disk.\n";
    const threadID = `thread_external_${suffix}`;
    const messageBody = "The external review changed with the document.";
    await writeFile(temporaryDocumentPath, updatedSource, "utf8");
    await writeFile(temporarySidecarPath, reviewSidecar(threadID, messageBody), "utf8");
    await rename(temporaryDocumentPath, originalDocumentPath);
    await rename(temporarySidecarPath, originalSidecarPath);

    const updatedState = await waitForChangedState(
      request,
      environment,
      createdState.workspaceRevision,
      (state) => {
        const entry = findDocument(state, originalPath);
        return (
          entry?.documentMetadataRevision !== createdEntry?.documentMetadataRevision &&
          entry?.reviewMetadataRevision !== createdEntry?.reviewMetadataRevision
        );
      },
      "document and sidecar replacement should publish new metadata revisions"
    );
    const updatedEntry = findDocument(updatedState, originalPath);
    expect(updatedEntry?.documentMetadataRevision).toMatch(metadataRevisionPattern);
    expect(updatedEntry?.reviewMetadataRevision).toMatch(metadataRevisionPattern);

    const updatedDocument = await readDocument(request, environment, originalPath);
    const updatedReview = await readReview(request, environment, originalPath);
    expect(updatedDocument.source).toBe(updatedSource);
    expect(updatedDocument.revision).not.toBe(initialDocument.revision);
    expect(updatedReview.documentRevision).toBe(updatedDocument.revision);
    expect(updatedReview.threads).toHaveLength(1);
    expect(updatedReview.threads[0]?.id).toBe(threadID);
    expect(updatedReview.threads[0]?.messages[0]?.body).toBe(messageBody);

    await rename(originalDocumentPath, renamedDocumentPath);
    await rename(originalSidecarPath, renamedSidecarPath);

    const renamedState = await waitForChangedState(
      request,
      environment,
      updatedState.workspaceRevision,
      (state) => {
        const renamedEntry = findDocument(state, renamedPath);
        return (
          findDocument(state, originalPath) === undefined && hasMetadataRevisions(renamedEntry)
        );
      },
      "renamed Markdown and sidecar should replace the old navigation identity"
    );
    expect(findDocument(renamedState, originalPath)).toBeUndefined();
    expect(findDocument(renamedState, renamedPath)).toBeDefined();

    await expectError(
      await request.get(
        `${environment.baseURL}/api/document?path=${encodeURIComponent(originalPath)}`,
        {
          headers: requestHeaders()
        }
      ),
      404,
      "documentNotFound"
    );
    const renamedDocument = await readDocument(request, environment, renamedPath);
    const renamedReview = await readReview(request, environment, renamedPath);
    expect(renamedDocument.source).toBe(updatedSource);
    expect(renamedReview.documentRevision).toBe(renamedDocument.revision);
    expect(renamedReview.threads[0]?.id).toBe(threadID);
  } finally {
    await removePaths(cleanupPaths);
  }
});

test("two browser contexts observe one published workspace revision", async ({
  browser,
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const suffix = projectSuffix(testInfo);
  const documentPath = `m5-shared-cache-${suffix}.md`;
  const absoluteDocumentPath = join(environment.workspace, documentPath);
  const absoluteSidecarPath = `${absoluteDocumentPath}.review.json`;
  const cleanupPaths = [absoluteDocumentPath, absoluteSidecarPath];
  let firstContext: BrowserContext | undefined;
  let secondContext: BrowserContext | undefined;

  await removePaths(cleanupPaths);
  try {
    const baseline = await readFullState(request, environment);
    firstContext = await browser.newContext({
      extraHTTPHeaders: requestHeaders()
    });
    secondContext = await browser.newContext({
      extraHTTPHeaders: requestHeaders()
    });
    await writeFile(absoluteDocumentPath, "# Shared scan\n", "utf8");
    await writeFile(absoluteSidecarPath, reviewSidecar(), "utf8");

    let observed:
      | {
          first: ChangedState;
          second: ChangedState;
        }
      | undefined;
    await expect
      .poll(
        async () => {
          if (!firstContext || !secondContext) {
            throw new Error("browser contexts were not created");
          }
          const [first, second] = await Promise.all([
            readState(firstContext.request, environment, baseline.workspaceRevision),
            readState(secondContext.request, environment, baseline.workspaceRevision)
          ]);
          if (
            first.status === "changed" &&
            second.status === "changed" &&
            first.workspaceRevision === second.workspaceRevision &&
            findDocument(first, documentPath) !== undefined &&
            findDocument(second, documentPath) !== undefined
          ) {
            observed = { first, second };
            return true;
          }
          return false;
        },
        {
          intervals: [0, 50, 100, 250, 500],
          message: "both contexts should receive the same newly published index",
          timeout: 8_000
        }
      )
      .toBe(true);

    if (!observed) {
      throw new Error("contexts completed polling without a shared changed state");
    }
    const firstEntry = findDocument(observed.first, documentPath);
    const secondEntry = findDocument(observed.second, documentPath);
    expect(observed.first.workspaceRevision).toBeGreaterThan(baseline.workspaceRevision);
    expect(secondEntry?.documentMetadataRevision).toBe(firstEntry?.documentMetadataRevision);
    expect(secondEntry?.reviewMetadataRevision).toBe(firstEntry?.reviewMetadataRevision);
  } finally {
    await firstContext?.close();
    await secondContext?.close();
    await removePaths(cleanupPaths);
  }
});
