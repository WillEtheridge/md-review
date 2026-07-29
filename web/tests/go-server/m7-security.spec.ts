import { createServer, type IncomingMessage } from "node:http";
import { readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";

import {
  expect,
  test,
  type APIRequestContext,
  type BrowserContext,
  type Page,
  type Request
} from "@playwright/test";

interface ReviewSnapshot {
  documentRevision: string;
  reviewRevision: string;
  targets: {
    threads: Record<string, string>;
  };
}

interface ServerEnvironment {
  baseURL: string;
  workspace: string;
}

interface AttackerRequest {
  origin: string | undefined;
  referer: string | undefined;
  url: string;
}

interface RuntimeNetworkAudit {
  assertAllowed: () => void;
  stop: () => void;
}

const allowedBrowserInternalRequestURLs = new Set(["about:blank"]);

function serverEnvironment(): ServerEnvironment {
  const baseURL = process.env.MDREVIEW_GO_SERVER_URL;
  const workspace = process.env.MDREVIEW_GO_SERVER_WORKSPACE;
  if (!baseURL || !workspace) {
    throw new Error("compiled mdReview server environment is unavailable");
  }
  return { baseURL, workspace };
}

function isAllowedRuntimeRequest(requestURL: string, mdReviewOrigin: string): boolean {
  if (allowedBrowserInternalRequestURLs.has(requestURL)) {
    return true;
  }
  try {
    return new URL(requestURL).origin === mdReviewOrigin;
  } catch {
    return false;
  }
}

function startRuntimeNetworkAudit(
  context: BrowserContext,
  environment: ServerEnvironment
): RuntimeNetworkAudit {
  const mdReviewOrigin = new URL(environment.baseURL).origin;
  const unexpectedRequests: string[] = [];
  const handleRequest = (request: Request): void => {
    if (!isAllowedRuntimeRequest(request.url(), mdReviewOrigin)) {
      unexpectedRequests.push(`${request.method()} ${request.resourceType()} ${request.url()}`);
    }
  };

  // Context-level observation includes workers and popups as well as the
  // initial page. Callers install it before navigation so an external
  // bootstrap, font, stylesheet, prefetch, or other attempted request cannot
  // occur before the release assertion starts.
  context.on("request", handleRequest);
  return {
    assertAllowed: () => {
      expect(
        unexpectedRequests,
        `packaged mdReview attempted requests outside ${mdReviewOrigin}`
      ).toEqual([]);
    },
    stop: () => {
      context.off("request", handleRequest);
    }
  };
}

function encodedIDSegment(id: string): string {
  return `~${Buffer.from(id, "utf8").toString("base64url")}`;
}

function headerValue(request: IncomingMessage, name: string): string | undefined {
  const value = request.headers[name];
  return Array.isArray(value) ? value.join(", ") : value;
}

async function startAttackerServer(): Promise<{
  baseURL: string;
  close: () => Promise<void>;
  requests: AttackerRequest[];
}> {
  const requests: AttackerRequest[] = [];
  const server = createServer((request, response) => {
    requests.push({
      origin: headerValue(request, "origin"),
      referer: headerValue(request, "referer"),
      url: request.url ?? "/"
    });
    response.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8"
    });
    response.end("<!doctype html><title>Attacker origin</title><h1>Attacker origin</h1>");
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("attacker server has no TCP address");
  }
  return {
    baseURL: `http://127.0.0.1:${String(address.port)}`,
    close: async () => {
      await new Promise<void>((resolve, reject) => {
        server.close((error) => {
          if (error) {
            reject(error);
          } else {
            resolve();
          }
        });
      });
    },
    requests
  };
}

async function waitForIndexedReview(
  request: APIRequestContext,
  environment: ServerEnvironment,
  documentPath: string
): Promise<ReviewSnapshot> {
  const deadline = Date.now() + 10_000;
  for (;;) {
    const headers = {};
    const state = await request.get(`${environment.baseURL}/api/state`, { headers });
    expect(state.status()).toBe(200);
    const review = await request.get(
      `${environment.baseURL}/api/review?path=${encodeURIComponent(documentPath)}`,
      { headers }
    );
    if (review.status() === 200) {
      return (await review.json()) as ReviewSnapshot;
    }
    expect(review.status()).toBe(404);
    if (Date.now() >= deadline) {
      throw new Error(`compiled server did not index ${documentPath}`);
    }
    await new Promise((resolve) => {
      setTimeout(resolve, 50);
    });
  }
}

async function openWorkspace(page: Page, environment: ServerEnvironment): Promise<void> {
  await page.goto(`${environment.baseURL}/`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Integration workspace" })
  ).toBeVisible();
}

async function submitCrossOriginForm(page: Page, route: string): Promise<void> {
  await page.evaluate((target) => {
    const frame = document.createElement("iframe");
    frame.name = "cross-origin-form-result";
    frame.hidden = true;
    document.body.append(frame);
    const form = document.createElement("form");
    form.action = target;
    form.method = "post";
    form.target = frame.name;
    const field = document.createElement("input");
    field.name = "operation";
    field.value = "forged";
    form.append(field);
    document.body.append(form);
    form.submit();
  }, route);
}

async function submitNullOriginForm(page: Page, route: string): Promise<void> {
  await page.evaluate((target) => {
    const frame = document.createElement("iframe");
    frame.sandbox.add("allow-forms", "allow-scripts");
    frame.hidden = true;
    frame.srcdoc =
      `<!doctype html><form method="post" action="${target}">` +
      '<input name="operation" value="forged"></form>' +
      "<script>document.forms[0].submit()</" +
      "script>";
    document.body.append(frame);
  }, route);
}

async function assertCrossOriginRejections(
  context: BrowserContext,
  attackerBaseURL: string,
  route: string,
  operation: object,
  unchangedSidecar: () => Promise<void>
): Promise<void> {
  const attackerPage = await context.newPage();
  try {
    await attackerPage.goto(attackerBaseURL);

    const crossOriginFetchRequest = attackerPage.waitForRequest(
      (request) => request.url() === route && request.method() === "PATCH"
    );
    const fetchResult = await attackerPage.evaluate(
      async ({ requestBody, target }) => {
        try {
          const response = await fetch(target, {
            body: JSON.stringify(requestBody),
            headers: {
              "Content-Type": "application/json"
            },
            method: "PATCH",
            mode: "cors"
          });
          return { kind: "response", status: response.status };
        } catch (error) {
          return { error: String(error), kind: "rejected" };
        }
      },
      {
        requestBody: operation,
        target: route
      }
    );
    const attemptedFetch = await crossOriginFetchRequest;
    expect(fetchResult.kind).toBe("rejected");
    expect(attemptedFetch.headers().authorization).toBeUndefined();
    expect(attemptedFetch.headers().referer).toBe(`${attackerBaseURL}/`);
    await unchangedSidecar();

    const formResponsePromise = attackerPage.waitForResponse(
      (response) => response.url() === route && response.request().method() === "POST"
    );
    await submitCrossOriginForm(attackerPage, route);
    const formResponse = await formResponsePromise;
    expect(formResponse.status()).toBeGreaterThanOrEqual(400);
    expect(formResponse.headers()["access-control-allow-origin"]).toBeUndefined();
    expect(formResponse.request().headers().origin).toBe(attackerBaseURL);
    await unchangedSidecar();

    const nullOriginResponsePromise = attackerPage.waitForResponse(
      (response) => response.url() === route && response.request().method() === "POST"
    );
    await submitNullOriginForm(attackerPage, route);
    const nullOriginResponse = await nullOriginResponsePromise;
    expect(nullOriginResponse.status()).toBeGreaterThanOrEqual(400);
    expect(nullOriginResponse.request().headers().origin).toBe("null");
    await unchangedSidecar();
  } finally {
    await attackerPage.close();
  }
}

test("m7 packaged browser stays within the local runtime network allowlist", async ({
  context,
  page
}) => {
  const environment = serverEnvironment();
  const audit = startRuntimeNetworkAudit(context, environment);

  try {
    const conditionalStateResponse = page.waitForResponse((response) => {
      const responseURL = new URL(response.url());
      return (
        responseURL.origin === new URL(environment.baseURL).origin &&
        responseURL.pathname === "/api/state" &&
        responseURL.searchParams.has("since")
      );
    });

    await openWorkspace(page, environment);
    await page.evaluate(async () => {
      await document.fonts.ready;
    });
    await conditionalStateResponse;
  } finally {
    audit.stop();
    audit.assertAllowed();
  }
});

test("m7 compiled browser rejects second-origin attacks and keeps navigation local", async ({
  context,
  page,
  request
}, testInfo) => {
  const environment = serverEnvironment();
  const attacker = await startAttackerServer();
  const documentPath = `m7-security-${testInfo.project.name}.md`;
  const documentFile = join(environment.workspace, documentPath);
  const sidecarFile = `${documentFile}.review.json`;
  const threadID = "thread_m7_security";
  const consoleMessages: string[] = [];
  page.on("console", (message) => {
    consoleMessages.push(message.text());
  });

  try {
    const markdown = [
      "# M7 security boundary",
      "",
      "This document proves the final origin boundary.",
      "",
      `[External verification](${attacker.baseURL}/capture)`,
      ""
    ].join("\n");
    const sidecar = `${JSON.stringify(
      {
        schemaVersion: 1,
        threads: [
          {
            id: threadID,
            anchor: { type: "document" },
            status: "open",
            messages: [
              {
                id: "message_m7_security",
                author: { type: "human", name: "Reviewer" },
                body: "Exercise the release origin boundary.",
                createdAt: "2026-07-29T12:00:00Z"
              }
            ]
          }
        ]
      },
      null,
      2
    )}\n`;
    await writeFile(documentFile, markdown, "utf8");
    await writeFile(sidecarFile, sidecar, "utf8");
    const review = await waitForIndexedReview(request, environment, documentPath);
    const fingerprint = review.targets.threads[threadID];
    expect(fingerprint).toMatch(/^[0-9a-f]{64}$/u);
    if (!fingerprint) {
      throw new Error("M7 security fixture has no thread fingerprint");
    }
    const route = `${environment.baseURL}/api/threads/${encodedIDSegment(threadID)}/status`;
    const operation = {
      documentPath,
      expectedDocumentRevision: review.documentRevision,
      expectedReviewRevision: review.reviewRevision,
      status: "resolved",
      targetFingerprint: fingerprint
    };
    const unchangedSidecar = async () => {
      expect(await readFile(sidecarFile, "utf8")).toBe(sidecar);
    };

    await assertCrossOriginRejections(
      context,
      attacker.baseURL,
      route,
      operation,
      unchangedSidecar
    );

    const explicitNullOrigin = await request.patch(route, {
      data: JSON.stringify(operation),
      headers: {
        Origin: "null",
        "Content-Type": "application/json"
      }
    });
    expect(explicitNullOrigin.status()).toBe(403);
    await expect(explicitNullOrigin.json()).resolves.toMatchObject({
      error: { code: "invalidOrigin" }
    });
    await unchangedSidecar();

    await openWorkspace(page, environment);
    expect(page.url()).toBe(`${environment.baseURL}/`);
    await page.getByRole("button", { name: documentPath, exact: true }).click();
    await expect(
      page.getByRole("heading", { level: 1, name: "M7 security boundary" })
    ).toBeVisible();
    const externalLink = page.getByRole("link", { name: "External verification" });
    const popupPromise = context.waitForEvent("page");
    await externalLink.click();
    const popup = await popupPromise;
    await popup.waitForLoadState();
    await popup.close();
    const capture = attacker.requests.find((candidate) => candidate.url === "/capture");
    expect(capture).toBeDefined();
    expect(capture?.referer ?? "").toBe("");

    const sameOriginMutation = await page.evaluate(
      async ({ requestBody, target }) => {
        const response = await fetch(target, {
          body: JSON.stringify(requestBody),
          headers: {
            "Content-Type": "application/json"
          },
          method: "PATCH"
        });
        return {
          body: (await response.json()) as unknown,
          status: response.status
        };
      },
      {
        requestBody: operation,
        target: route
      }
    );
    expect(sameOriginMutation.status).toBe(200);
    expect(sameOriginMutation.body).toMatchObject({
      durability: "durable",
      thread: { id: threadID, status: "resolved" }
    });
    expect(await readFile(sidecarFile, "utf8")).toContain('"status": "resolved"');
  } finally {
    await rm(documentFile, { force: true });
    await rm(sidecarFile, { force: true });
    await attacker.close();
  }
});
