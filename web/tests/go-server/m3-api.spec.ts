import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { expect, test, type APIRequestContext, type APIResponse } from "@playwright/test";

interface ReviewMessage {
  id: string;
  author: {
    type: "human" | "agent";
    name: string;
  };
  body: string;
  createdAt: string;
}

interface ReviewThread {
  id: string;
  status: "open" | "handled" | "resolved";
  messages: ReviewMessage[];
}

interface ReviewSnapshot {
  documentRevision: string;
  reviewRevision: string;
  threads: ReviewThread[];
}

interface MutationResponse {
  documentRevision: string;
  reviewRevision: string;
  thread: ReviewThread;
}

interface ServerEnvironment {
  baseURL: string;
  workspace: string;
}

function serverEnvironment(): ServerEnvironment {
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

function mutationHeaders(environment: ServerEnvironment): Record<string, string> {
  return {
    Origin: environment.baseURL,
    "Content-Type": "application/json"
  };
}

function encodedIDSegment(id: string): string {
  return `~${Buffer.from(id, "utf8").toString("base64url")}`;
}

function findThread(review: ReviewSnapshot, id: string): ReviewThread {
  const thread = review.threads.find((candidate) => candidate.id === id);
  expect(thread, `missing thread ${JSON.stringify(id)}`).toBeDefined();
  if (!thread) {
    throw new Error(`missing thread ${JSON.stringify(id)}`);
  }
  return thread;
}

function findMessage(thread: ReviewThread, id: string): ReviewMessage {
  const message = thread.messages.find((candidate) => candidate.id === id);
  expect(message, `missing message ${JSON.stringify(id)}`).toBeDefined();
  if (!message) {
    throw new Error(`missing message ${JSON.stringify(id)}`);
  }
  return message;
}

async function readReview(
  request: APIRequestContext,
  environment: ServerEnvironment,
  documentPath: string
): Promise<ReviewSnapshot> {
  const response = await request.get(
    `${environment.baseURL}/api/review?path=${encodeURIComponent(documentPath)}`,
    {
      headers: {}
    }
  );
  expect(response.status()).toBe(200);
  return (await response.json()) as ReviewSnapshot;
}

async function expectError(
  response: APIResponse,
  status: number,
  code: string
): Promise<Record<string, unknown>> {
  expect(response.status()).toBe(status);
  const body = (await response.json()) as {
    error?: {
      code?: string;
    };
  } & Record<string, unknown>;
  expect(body.error?.code).toBe(code);
  return body;
}

function commonRevisionRequest(
  documentPath: string,
  review: ReviewSnapshot
): {
  documentPath: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string;
} {
  return {
    documentPath,
    expectedDocumentRevision: review.documentRevision,
    expectedReviewRevision: review.reviewRevision
  };
}

function sha256(content: string | Buffer): string {
  return createHash("sha256").update(content).digest("hex");
}

async function replaceExactly(path: string, before: string, after: string): Promise<void> {
  const content = await readFile(path, "utf8");
  const first = content.indexOf(before);
  expect(first, `fixture text ${JSON.stringify(before)} is missing`).toBeGreaterThanOrEqual(0);
  expect(content.indexOf(before, first + before.length), "fixture replacement is ambiguous").toBe(
    -1
  );
  await writeFile(path, content.slice(0, first) + after + content.slice(first + before.length));
}

test("compiled server completes the human lifecycle and persists only sidecar data", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-workflow", testInfo.project.name);
  const sidecarPath = join(environment.workspace, `${documentPath}.review.json`);
  let review = await readReview(request, environment, documentPath);
  expect(findThread(review, "thread_workflow").status).toBe("handled");

  const replyResponse = await request.post(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_workflow")}/messages`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, review),
        message: {
          body: "A human follow-up from the compiled API."
        }
      })
    }
  );
  expect(replyResponse.status()).toBe(201);
  let mutation = (await replyResponse.json()) as MutationResponse;
  expect(mutation.thread.status).toBe("open");
  expect(mutation.thread.messages).toHaveLength(2);
  expect(mutation.thread.messages[1]).toMatchObject({
    author: {
      type: "human",
      name: "Reviewer"
    },
    body: "A human follow-up from the compiled API."
  });
  expect(mutation.thread.messages[1]?.id).toMatch(/^message_[A-Za-z0-9_-]{22,}$/u);

  const resolveResponse = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_workflow")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: mutation.documentRevision,
        expectedReviewRevision: mutation.reviewRevision,
        status: "resolved"
      })
    }
  );
  expect(resolveResponse.status()).toBe(200);
  mutation = (await resolveResponse.json()) as MutationResponse;
  expect(mutation.thread.status).toBe("resolved");

  const reopenResponse = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_workflow")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: mutation.documentRevision,
        expectedReviewRevision: mutation.reviewRevision,
        status: "open"
      })
    }
  );
  expect(reopenResponse.status()).toBe(200);
  mutation = (await reopenResponse.json()) as MutationResponse;
  expect(mutation.thread.status).toBe("open");

  for (const status of ["handled", "open"] as const) {
    const invalidStatus = await request.patch(
      `${environment.baseURL}/api/threads/${encodedIDSegment("thread_workflow")}/status`,
      {
        headers: mutationHeaders(environment),
        data: JSON.stringify({
          documentPath,
          expectedDocumentRevision: mutation.documentRevision,
          expectedReviewRevision: mutation.reviewRevision,
          status
        })
      }
    );
    await expectError(invalidStatus, 422, "invalidReviewOperation");
  }

  review = await readReview(request, environment, documentPath);
  const repliedDelete = await request.delete(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_agent")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, review)
      })
    }
  );
  await expectError(repliedDelete, 422, "invalidReviewOperation");

  const deleteResponse = await request.delete(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_delete")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, review)
      })
    }
  );
  expect(deleteResponse.status()).toBe(200);
  const deleted = (await deleteResponse.json()) as {
    documentRevision: string;
    reviewRevision: string;
    deletedThreadId: string;
  };
  expect(deleted).toMatchObject({
    documentRevision: review.documentRevision,
    deletedThreadId: "thread_delete"
  });

  const restored = await readReview(request, environment, documentPath);
  expect(restored.threads.map((thread) => thread.id)).not.toContain("thread_delete");
  expect(findThread(restored, "thread_workflow").status).toBe("open");
  expect(findMessage(findThread(restored, "thread_workflow"), "message_human").body).toBe(
    "Original human feedback."
  );
  expect(findMessage(findThread(restored, "thread_agent"), "message_agent").body).toBe(
    "Agent-authored explanation."
  );

  const exactSidecar = await readFile(sidecarPath, "utf8");
  expect(sha256(exactSidecar)).toBe(restored.reviewRevision);
  for (const transportOnlyField of ['"attachment"', '"documentRevision"', '"reviewRevision"']) {
    expect(exactSidecar).not.toContain(transportOnlyField);
  }
});

test("compiled server rejects every stale sidecar revision without writing", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-merge", testInfo.project.name);
  const sidecarPath = join(environment.workspace, `${documentPath}.review.json`);
  const initial = await readReview(request, environment, documentPath);

  await replaceExactly(
    sidecarPath,
    "Unrelated original body.",
    "Unrelated externally changed body."
  );
  const staleReply = await request.post(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_target")}/messages`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, initial),
        message: {
          body: "This reply must not merge with an external sidecar change."
        }
      })
    }
  );
  const conflict = (await expectError(staleReply, 409, "reviewChanged")) as unknown as {
    current: {
      documentRevision: string;
      reviewRevision: string | null;
    };
  };
  const externallyChangedSidecar = await readFile(sidecarPath, "utf8");
  expect(conflict.current.documentRevision).toBe(initial.documentRevision);
  expect(conflict.current.reviewRevision).toBe(sha256(await readFile(sidecarPath, "utf8")));
  expect(externallyChangedSidecar).toContain("Unrelated externally changed body.");
  expect(externallyChangedSidecar).not.toContain(
    "This reply must not merge with an external sidecar change."
  );

  const afterConflict = await readReview(request, environment, documentPath);
  expect(findThread(afterConflict, "thread_target").status).toBe("open");
  expect(findMessage(findThread(afterConflict, "thread_target"), "message_target").body).toBe(
    "Target original body."
  );
});

test("compiled server routes opaque IDs as one base64url segment", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-opaque", testInfo.project.name);
  const threadID = "./..//100%/雪";
  const threadSegment = encodedIDSegment(threadID);
  expect(threadSegment).toMatch(/^~[A-Za-z0-9_-]+$/u);

  const initial = await readReview(request, environment, documentPath);
  const replyResponse = await request.post(
    `${environment.baseURL}/api/threads/${threadSegment}/messages`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, initial),
        message: {
          body: "The encoded route kept every opaque character."
        }
      })
    }
  );
  expect(replyResponse.status()).toBe(201);
  const replied = (await replyResponse.json()) as MutationResponse;
  expect(replied.thread.id).toBe(threadID);
});

test("compiled server scopes identical target IDs to the requested document", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentA = browserDocument("m3-scoped-a", testInfo.project.name);
  const documentB = browserDocument("m3-scoped-b", testInfo.project.name);
  const sidecarBPath = join(environment.workspace, `${documentB}.review.json`);
  const beforeB = await readFile(sidecarBPath, "utf8");
  const reviewA = await readReview(request, environment, documentA);
  const resolveA = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_shared")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentA, reviewA),
        status: "resolved"
      })
    }
  );
  expect(resolveA.status()).toBe(200);
  expect(((await resolveA.json()) as MutationResponse).thread.status).toBe("resolved");

  const restoredA = await readReview(request, environment, documentA);
  const restoredB = await readReview(request, environment, documentB);
  expect(findThread(restoredA, "thread_shared").status).toBe("resolved");
  expect(findThread(restoredB, "thread_shared").status).toBe("open");
  expect(findMessage(findThread(restoredB, "thread_shared"), "message_shared").body).toBe(
    "Document B feedback."
  );
  expect(await readFile(sidecarBPath, "utf8")).toBe(beforeB);
});

test("compiled server enforces mutation media, origin, and limits", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-security", testInfo.project.name);
  const sidecarPath = join(environment.workspace, `${documentPath}.review.json`);
  const before = await readFile(sidecarPath, "utf8");
  const review = await readReview(request, environment, documentPath);
  const route = `${environment.baseURL}/api/threads/${encodedIDSegment("thread_security")}/messages`;
  const operation = {
    ...commonRevisionRequest(documentPath, review),
    message: {
      body: "A valid reply."
    }
  };

  await expectError(
    await request.post(route, {
      headers: {
        ...mutationHeaders(environment),
        Origin: "http://localhost.invalid"
      },
      data: JSON.stringify(operation)
    }),
    403,
    "invalidOrigin"
  );
  await expectError(
    await request.post(route, {
      headers: {
        "Content-Type": "application/json"
      },
      data: JSON.stringify(operation)
    }),
    403,
    "invalidOrigin"
  );
  await expectError(
    await request.post(route, {
      headers: {
        ...mutationHeaders(environment),
        "Content-Type": "text/plain"
      },
      data: JSON.stringify(operation)
    }),
    415,
    "unsupportedMediaType"
  );
  await expectError(
    await request.post(route, {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...operation,
        message: {
          body: "x".repeat(2 * 1024 * 1024)
        }
      })
    }),
    413,
    "requestTooLarge"
  );
  await expectError(
    await request.post(route, {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...operation,
        message: {
          body: "é".repeat(32 * 1024 + 1)
        }
      })
    }),
    422,
    "invalidReviewOperation"
  );
  await expectError(
    await request.post(route, {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...operation,
        message: {
          body: "A browser cannot choose an agent author.",
          author: {
            type: "agent",
            name: "Injected"
          }
        }
      })
    }),
    422,
    "invalidReviewOperation"
  );
  expect(await readFile(sidecarPath, "utf8")).toBe(before);
});

test("compiled server accepts a direct agent reply before human resolution", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-external-agent", testInfo.project.name);
  const sidecarPath = join(environment.workspace, `${documentPath}.review.json`);
  const sidecar = JSON.parse(await readFile(sidecarPath, "utf8")) as {
    schemaVersion: number;
    threads: ReviewThread[];
  };
  const thread = sidecar.threads[0];
  expect(thread).toBeDefined();
  if (!thread) {
    throw new Error("external-agent fixture has no thread");
  }
  thread.messages.push({
    id: "message_external_agent",
    author: {
      type: "agent",
      name: "Codex"
    },
    body: "Implemented the requested clarification.",
    createdAt: "2026-07-29T09:30:00Z"
  });
  thread.status = "handled";
  await writeFile(sidecarPath, `${JSON.stringify(sidecar, null, 2)}\n`);

  const externallyUpdated = await readReview(request, environment, documentPath);
  expect(findThread(externallyUpdated, "thread_external").status).toBe("handled");
  expect(
    findMessage(findThread(externallyUpdated, "thread_external"), "message_external_agent")
  ).toMatchObject({
    author: {
      type: "agent",
      name: "Codex"
    },
    body: "Implemented the requested clarification."
  });

  const resolveResponse = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_external")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonRevisionRequest(documentPath, externallyUpdated),
        status: "resolved"
      })
    }
  );
  expect(resolveResponse.status()).toBe(200);
  const resolved = (await resolveResponse.json()) as MutationResponse;
  expect(resolved.thread.status).toBe("resolved");
  expect(findMessage(resolved.thread, "message_external_agent").body).toBe(
    "Implemented the requested clarification."
  );

  const persisted = JSON.parse(await readFile(sidecarPath, "utf8")) as {
    threads: ReviewThread[];
  };
  expect(persisted.threads[0]?.status).toBe("resolved");
  expect(persisted.threads[0]?.messages[1]?.author.type).toBe("agent");
});
