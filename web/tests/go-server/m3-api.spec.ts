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
  editedAt?: string;
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
  targets: {
    threads: Record<string, string>;
    messages: Record<string, string>;
  };
}

interface MutationResponse {
  documentRevision: string;
  reviewRevision: string;
  durability: "durable" | "uncertain";
  thread: ReviewThread;
  targets: {
    threads: Record<string, string>;
    messages: Record<string, string>;
  };
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

function targetFingerprint(targets: Record<string, string>, id: string): string {
  const fingerprint = targets[id];
  expect(fingerprint, `missing fingerprint for ${JSON.stringify(id)}`).toMatch(/^[0-9a-f]{64}$/u);
  if (!fingerprint) {
    throw new Error(`missing fingerprint for ${JSON.stringify(id)}`);
  }
  return fingerprint;
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

function commonTargetRequest(
  documentPath: string,
  review: ReviewSnapshot,
  fingerprint: string
): {
  documentPath: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string;
  targetFingerprint: string;
} {
  return {
    documentPath,
    expectedDocumentRevision: review.documentRevision,
    expectedReviewRevision: review.reviewRevision,
    targetFingerprint: fingerprint
  };
}

function sha256(content: string | Buffer): string {
  return createHash("sha256").update(content).digest("hex");
}

function exactTargetJSON(sidecar: string, id: string): string {
  const marker = `"id": ${JSON.stringify(id)}`;
  const idPosition = sidecar.indexOf(marker);
  if (idPosition < 0) {
    throw new Error(`sidecar has no target ${JSON.stringify(id)}`);
  }
  const objectStart = sidecar.lastIndexOf("{", idPosition);
  if (objectStart < 0) {
    throw new Error(`sidecar target ${JSON.stringify(id)} has no object start`);
  }

  let depth = 0;
  let insideString = false;
  let escaped = false;
  for (let position = objectStart; position < sidecar.length; position += 1) {
    const character = sidecar[position];
    if (insideString) {
      if (escaped) {
        escaped = false;
      } else if (character === "\\") {
        escaped = true;
      } else if (character === '"') {
        insideString = false;
      }
      continue;
    }
    if (character === '"') {
      insideString = true;
    } else if (character === "{") {
      depth += 1;
    } else if (character === "}") {
      depth -= 1;
      if (depth === 0) {
        return sidecar.slice(objectStart, position + 1);
      }
    }
  }
  throw new Error(`sidecar target ${JSON.stringify(id)} has no object end`);
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
        ...commonTargetRequest(
          documentPath,
          review,
          targetFingerprint(review.targets.threads, "thread_workflow")
        ),
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
  expect(mutation.durability).toBe("durable");

  const originalCreatedAt = findMessage(mutation.thread, "message_human").createdAt;
  const editResponse = await request.patch(
    `${environment.baseURL}/api/messages/${encodedIDSegment("message_human")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: mutation.documentRevision,
        expectedReviewRevision: mutation.reviewRevision,
        targetFingerprint: targetFingerprint(mutation.targets.messages, "message_human"),
        message: {
          body: "Edited human feedback."
        }
      })
    }
  );
  expect(editResponse.status()).toBe(200);
  mutation = (await editResponse.json()) as MutationResponse;
  expect(findMessage(mutation.thread, "message_human")).toMatchObject({
    body: "Edited human feedback.",
    createdAt: originalCreatedAt,
    author: {
      type: "human",
      name: "Reviewer"
    },
    editedAt: expect.stringMatching(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/u)
  });

  const resolveResponse = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_workflow")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: mutation.documentRevision,
        expectedReviewRevision: mutation.reviewRevision,
        targetFingerprint: targetFingerprint(mutation.targets.threads, "thread_workflow"),
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
        targetFingerprint: targetFingerprint(mutation.targets.threads, "thread_workflow"),
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
          targetFingerprint: targetFingerprint(mutation.targets.threads, "thread_workflow"),
          status
        })
      }
    );
    await expectError(invalidStatus, 422, "invalidReviewOperation");
  }

  review = await readReview(request, environment, documentPath);
  const agentEdit = await request.patch(
    `${environment.baseURL}/api/messages/${encodedIDSegment("message_agent")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentPath,
          review,
          targetFingerprint(review.targets.messages, "message_agent")
        ),
        message: {
          body: "A browser must not edit this."
        }
      })
    }
  );
  await expectError(agentEdit, 422, "invalidReviewOperation");

  const repliedDelete = await request.delete(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_agent")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentPath,
          review,
          targetFingerprint(review.targets.threads, "thread_agent")
        )
      })
    }
  );
  await expectError(repliedDelete, 422, "invalidReviewOperation");

  const deleteResponse = await request.delete(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_delete")}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentPath,
          review,
          targetFingerprint(review.targets.threads, "thread_delete")
        )
      })
    }
  );
  expect(deleteResponse.status()).toBe(200);
  const deleted = (await deleteResponse.json()) as {
    documentRevision: string;
    reviewRevision: string;
    durability: string;
    deletedThreadId: string;
  };
  expect(deleted).toMatchObject({
    documentRevision: review.documentRevision,
    durability: "durable",
    deletedThreadId: "thread_delete"
  });

  const restored = await readReview(request, environment, documentPath);
  expect(restored.threads.map((thread) => thread.id)).not.toContain("thread_delete");
  expect(findThread(restored, "thread_workflow").status).toBe("open");
  expect(findMessage(findThread(restored, "thread_workflow"), "message_human").body).toBe(
    "Edited human feedback."
  );
  expect(findMessage(findThread(restored, "thread_agent"), "message_agent").body).toBe(
    "Agent-authored explanation."
  );

  const exactSidecar = await readFile(sidecarPath, "utf8");
  expect(sha256(exactSidecar)).toBe(restored.reviewRevision);
  expect(sha256(exactTargetJSON(exactSidecar, "thread_workflow"))).toBe(
    targetFingerprint(restored.targets.threads, "thread_workflow")
  );
  expect(sha256(exactTargetJSON(exactSidecar, "message_human"))).toBe(
    targetFingerprint(restored.targets.messages, "message_human")
  );
  for (const transportOnlyField of [
    '"attachment"',
    '"targets"',
    '"targetFingerprint"',
    '"documentRevision"',
    '"reviewRevision"'
  ]) {
    expect(exactSidecar).not.toContain(transportOnlyField);
  }
});

test("compiled server merges unrelated external changes and rejects a changed target", async ({
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
  const mergedResponse = await request.post(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_target")}/messages`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentPath,
          initial,
          targetFingerprint(initial.targets.threads, "thread_target")
        ),
        message: {
          body: "Merge this reply beside the unrelated external change."
        }
      })
    }
  );
  expect(mergedResponse.status()).toBe(201);
  const merged = (await mergedResponse.json()) as MutationResponse;
  const mergedSidecar = await readFile(sidecarPath, "utf8");
  expect(mergedSidecar).toContain("Unrelated externally changed body.");
  expect(mergedSidecar).toContain("Merge this reply beside the unrelated external change.");

  await replaceExactly(sidecarPath, "Target original body.", "Target externally changed body.");
  const targetConflict = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_target")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: merged.documentRevision,
        expectedReviewRevision: merged.reviewRevision,
        targetFingerprint: targetFingerprint(merged.targets.threads, "thread_target"),
        status: "resolved"
      })
    }
  );
  const conflict = (await expectError(targetConflict, 409, "targetChanged")) as unknown as {
    current: {
      documentRevision: string;
      reviewRevision: string;
      targetFingerprint: string | null;
    };
  };
  expect(conflict.current.documentRevision).toBe(merged.documentRevision);
  expect(conflict.current.reviewRevision).toBe(sha256(await readFile(sidecarPath, "utf8")));
  expect(conflict.current.targetFingerprint).toBe(
    sha256(exactTargetJSON(await readFile(sidecarPath, "utf8"), "thread_target"))
  );
  expect(conflict.current.targetFingerprint).not.toBe(
    targetFingerprint(merged.targets.threads, "thread_target")
  );

  const afterConflict = await readReview(request, environment, documentPath);
  expect(findThread(afterConflict, "thread_target").status).toBe("open");
  expect(findMessage(findThread(afterConflict, "thread_target"), "message_target").body).toBe(
    "Target externally changed body."
  );
});

test("compiled server routes opaque IDs as one base64url segment", async ({
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const documentPath = browserDocument("m3-opaque", testInfo.project.name);
  const threadID = "./..//100%/雪";
  const messageID = "../message/%/猫";
  const threadSegment = encodedIDSegment(threadID);
  const messageSegment = encodedIDSegment(messageID);
  expect(threadSegment).toMatch(/^~[A-Za-z0-9_-]+$/u);
  expect(messageSegment).toMatch(/^~[A-Za-z0-9_-]+$/u);

  const initial = await readReview(request, environment, documentPath);
  const replyResponse = await request.post(
    `${environment.baseURL}/api/threads/${threadSegment}/messages`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentPath,
          initial,
          targetFingerprint(initial.targets.threads, threadID)
        ),
        message: {
          body: "The encoded route kept every opaque character."
        }
      })
    }
  );
  expect(replyResponse.status()).toBe(201);
  const replied = (await replyResponse.json()) as MutationResponse;
  expect(replied.thread.id).toBe(threadID);

  const editResponse = await request.patch(
    `${environment.baseURL}/api/messages/${messageSegment}`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        documentPath,
        expectedDocumentRevision: replied.documentRevision,
        expectedReviewRevision: replied.reviewRevision,
        targetFingerprint: targetFingerprint(replied.targets.messages, messageID),
        message: {
          body: "Opaque message ID edited safely."
        }
      })
    }
  );
  expect(editResponse.status()).toBe(200);
  const edited = (await editResponse.json()) as MutationResponse;
  expect(findMessage(edited.thread, messageID).body).toBe("Opaque message ID edited safely.");
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
  const reviewB = await readReview(request, environment, documentB);
  expect(targetFingerprint(reviewA.targets.threads, "thread_shared")).not.toBe(
    targetFingerprint(reviewB.targets.threads, "thread_shared")
  );

  const resolveA = await request.patch(
    `${environment.baseURL}/api/threads/${encodedIDSegment("thread_shared")}/status`,
    {
      headers: mutationHeaders(environment),
      data: JSON.stringify({
        ...commonTargetRequest(
          documentA,
          reviewA,
          targetFingerprint(reviewA.targets.threads, "thread_shared")
        ),
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
    ...commonTargetRequest(
      documentPath,
      review,
      targetFingerprint(review.targets.threads, "thread_security")
    ),
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
        ...commonTargetRequest(
          documentPath,
          externallyUpdated,
          targetFingerprint(externallyUpdated.targets.threads, "thread_external")
        ),
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
