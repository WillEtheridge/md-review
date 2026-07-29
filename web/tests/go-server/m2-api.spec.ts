import { readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { expect, test } from "@playwright/test";

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

function browserDocument(prefix: string, projectName: string): string {
  return `${prefix}-${projectName}.md`;
}

test("compiled server creates only the adjacent schema-v1 sidecar", async ({
  request
}, testInfo) => {
  const { baseURL, workspace } = serverEnvironment();
  const documentPath = browserDocument("api", testInfo.project.name);

  const documentResponse = await request.get(
    `${baseURL}/api/document?path=${encodeURIComponent(documentPath)}`
  );
  expect(documentResponse.status()).toBe(200);
  const document = (await documentResponse.json()) as {
    revision: string;
  };

  const emptyResponse = await request.get(
    `${baseURL}/api/review?path=${encodeURIComponent(documentPath)}`
  );
  expect(emptyResponse.status()).toBe(200);
  expect(await emptyResponse.json()).toMatchObject({
    path: documentPath,
    documentRevision: document.revision,
    reviewRevision: null,
    threads: []
  });

  const createResponse = await request.post(`${baseURL}/api/threads`, {
    headers: {
      Origin: baseURL,
      "Content-Type": "application/json"
    },
    data: JSON.stringify({
      documentPath,
      expectedDocumentRevision: document.revision,
      expectedReviewRevision: null,
      anchor: {
        type: "document"
      },
      message: {
        body: "Add an API-focused summary."
      }
    })
  });
  expect(createResponse.status()).toBe(201);
  const created = (await createResponse.json()) as {
    documentRevision: string;
    reviewRevision: string;
    durability: string;
    thread: {
      id: string;
      status: string;
      attachment: { state: string };
      messages: Array<{
        id: string;
        author: { type: string; name: string };
        body: string;
      }>;
    };
  };
  expect(created.documentRevision).toBe(document.revision);
  expect(created.reviewRevision).toMatch(/^[0-9a-f]{64}$/u);
  expect(created.durability).toBe("durable");
  expect(created.thread.id).toMatch(/^thread_[A-Za-z0-9_-]{22,}$/u);
  expect(created.thread.status).toBe("open");
  expect(created.thread.attachment.state).toBe("document");
  expect(created.thread.messages).toHaveLength(1);
  expect(created.thread.messages[0]?.id).toMatch(/^message_[A-Za-z0-9_-]{22,}$/u);
  expect(created.thread.messages[0]?.author).toEqual({
    type: "human",
    name: "Reviewer"
  });

  const sidecarPath = join(workspace, `${documentPath}.review.json`);
  const sidecarMode = (await stat(sidecarPath)).mode & 0o777;
  expect(sidecarMode & 0o022).toBe(0);
  const sidecar = JSON.parse(await readFile(sidecarPath, "utf8")) as {
    schemaVersion: number;
    threads: Array<{
      id: string;
      attachment?: unknown;
      messages: Array<{ body: string }>;
    }>;
  };
  expect(sidecar.schemaVersion).toBe(1);
  expect(sidecar.threads).toHaveLength(1);
  expect(sidecar.threads[0]?.id).toBe(created.thread.id);
  expect(sidecar.threads[0]?.attachment).toBeUndefined();
  expect(sidecar.threads[0]?.messages[0]?.body).toBe("Add an API-focused summary.");

  const restoredResponse = await request.get(
    `${baseURL}/api/review?path=${encodeURIComponent(documentPath)}`
  );
  expect(restoredResponse.status()).toBe(200);
  expect(await restoredResponse.json()).toMatchObject({
    path: documentPath,
    documentRevision: document.revision,
    reviewRevision: created.reviewRevision,
    threads: [
      {
        id: created.thread.id,
        attachment: { state: "document" }
      }
    ]
  });
});

test("compiled server enforces review safety and mutation boundaries", async ({
  request
}, testInfo) => {
  const { baseURL, workspace } = serverEnvironment();
  for (const [documentPath, status, code] of [
    ["invalid-review.md", 422, "reviewInvalid"],
    ["large-review.md", 413, "reviewTooLarge"],
    ["unsafe-review.md", 422, "reviewUnsafe"]
  ] as const) {
    const response = await request.get(
      `${baseURL}/api/review?path=${encodeURIComponent(documentPath)}`
    );
    expect(response.status()).toBe(status);
    expect(await response.json()).toMatchObject({ error: { code } });
  }

  const documentPath = browserDocument("conflict-api", testInfo.project.name);
  const documentResponse = await request.get(
    `${baseURL}/api/document?path=${encodeURIComponent(documentPath)}`
  );
  const document = (await documentResponse.json()) as {
    revision: string;
    source: string;
  };
  const selectedSource = "removable phrase";
  const start = document.source.indexOf(selectedSource);
  expect(start).toBeGreaterThanOrEqual(0);
  await writeFile(
    join(workspace, documentPath),
    document.source.replace(selectedSource, "replacement wording"),
    "utf8"
  );

  const operation = {
    documentPath,
    expectedDocumentRevision: document.revision,
    expectedReviewRevision: null,
    anchor: {
      type: "text",
      range: {
        start,
        end: start + Buffer.byteLength(selectedSource)
      },
      source: selectedSource,
      text: selectedSource
    },
    message: {
      body: "This must remain a draft."
    }
  };
  const conflict = await request.post(`${baseURL}/api/threads`, {
    headers: {
      Origin: baseURL,
      "Content-Type": "application/json"
    },
    data: JSON.stringify(operation)
  });
  expect(conflict.status()).toBe(409);
  expect(await conflict.json()).toMatchObject({
    error: { code: "documentChanged" },
    current: {
      documentRevision: expect.stringMatching(/^[0-9a-f]{64}$/u),
      reviewRevision: null
    }
  });

  const wrongOrigin = await request.post(`${baseURL}/api/threads`, {
    headers: {
      Origin: "http://localhost.invalid",
      "Content-Type": "application/json"
    },
    data: JSON.stringify(operation)
  });
  expect(wrongOrigin.status()).toBe(403);
  expect(await wrongOrigin.json()).toMatchObject({ error: { code: "invalidOrigin" } });

  const wrongType = await request.post(`${baseURL}/api/threads`, {
    headers: {
      Origin: baseURL,
      "Content-Type": "text/plain"
    },
    data: JSON.stringify(operation)
  });
  expect(wrongType.status()).toBe(415);
  expect(await wrongType.json()).toMatchObject({
    error: { code: "unsupportedMediaType" }
  });

  const forgedPath = await request.post(`${baseURL}/api/threads`, {
    headers: {
      Origin: baseURL,
      "Content-Type": "application/json"
    },
    data: JSON.stringify({
      ...operation,
      documentPath: "../outside-secret.md",
      anchor: { type: "document" }
    })
  });
  expect(forgedPath.status()).toBe(400);
  expect(await forgedPath.json()).toMatchObject({
    error: { code: "invalidDocumentPath" }
  });
});
