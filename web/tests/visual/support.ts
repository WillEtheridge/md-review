import { expect, type Page, type Route } from "@playwright/test";

export type VisualTheme = "light" | "dark";

interface VisualThread {
  id: string;
  anchor: unknown;
  attachment: unknown;
  status: "open" | "handled" | "resolved";
  messages: unknown[];
}

interface VisualState {
  source: string;
  threads: VisualThread[];
  documentFailure?: {
    status: 404 | 413 | 422 | 500;
    code: "documentInvalidUtf8" | "documentNotFound" | "documentTooLarge" | "documentUnavailable";
    message: string;
  };
  reviewFailure?: {
    status: 413 | 422 | 500;
    code:
      | "reviewInvalid"
      | "reviewTooLarge"
      | "reviewUnavailable"
      | "reviewUnsafe"
      | "reviewUnsupportedSchema";
    message: string;
  };
  targetConflict?: boolean;
}

interface VisualSession {
  externalRequests: string[];
}

interface ByteRange {
  start: number;
  end: number;
}

const baseOrigin = "http://127.0.0.1:4173";
const documentPath = "docs/reading-experience.md";
const documentRevision = "d0820a0afd1e1aa6b8bbf91c8f6915e6d544eec8be1c032f7779a5e6a6b7b908";
const reviewRevision = "c768362a5157f9fc009c0a726a5e32b2ab46df1a18d1819a0344f30a8f21b86f";

export const richMarkdown = `# Designing an honest review workflow

A reading tool should put **evidence before convenience** and make uncertain
states visible. The document remains the source of truth, while comments live
beside it.

> Exact anchoring is deliberately conservative: when text cannot be identified
> uniquely, the review stays visible but detached.

## Working principles

1. Preserve the author’s Markdown.
2. Keep review data local and inspectable.
3. Prefer an explicit conflict over a silent overwrite.

- [x] Safe rendered Markdown
- [x] Threaded human feedback
- [ ] Browser-triggered external refresh

## Boundary summary

| Boundary | Owner | Observable result |
| --- | --- | --- |
| Markdown bytes | Repository | Never edited by mdReview |
| Review sidecar | Review store | Atomic semantic mutations |
| Browser selection | Source mapper | Exact UTF-8 byte range |
| External agent | User workflow | Direct, explicit file access |

Use \`Ctrl+Enter\` to save a comment. The lifecycle stays intentionally small:
\`open\`, \`handled\`, then human-accepted \`resolved\`.

\`\`\`go
if matches != 1 {
    return Detached
}
return Attached
\`\`\`

---

[Read the project documentation](../README.md)

![Architecture diagram](architecture.svg?inert=1)
`;

function byteRange(source: string, selectedText: string): ByteRange {
  const startUTF16 = source.indexOf(selectedText);
  if (startUTF16 < 0) {
    throw new Error(`visual fixture is missing selected text: ${selectedText}`);
  }
  const encoder = new TextEncoder();
  return {
    start: encoder.encode(source.slice(0, startUTF16)).length,
    end: encoder.encode(source.slice(0, startUTF16 + selectedText.length)).length
  };
}

function humanMessage(id: string, body: string): unknown {
  return {
    id,
    author: {
      type: "human",
      name: "Reviewer"
    },
    body,
    createdAt: "2026-07-28T14:30:00Z"
  };
}

function agentMessage(id: string, body: string): unknown {
  return {
    id,
    author: {
      type: "agent",
      name: "Codex"
    },
    body,
    createdAt: "2026-07-28T15:10:00Z"
  };
}

function documentThread(
  id: string,
  status: VisualThread["status"],
  messages: unknown[]
): VisualThread {
  return {
    id,
    anchor: {
      type: "document"
    },
    attachment: {
      state: "document"
    },
    status,
    messages
  };
}

function attachedThread(
  id: string,
  selectedText: string,
  status: VisualThread["status"],
  messages: unknown[]
): VisualThread {
  const range = byteRange(richMarkdown, selectedText);
  return {
    id,
    anchor: {
      type: "text",
      range,
      source: selectedText,
      text: selectedText
    },
    attachment: {
      state: "attached",
      currentRange: range
    },
    status,
    messages
  };
}

function detachedThread(): VisualThread {
  return {
    id: "thread_detached",
    anchor: {
      type: "text",
      range: {
        start: 132,
        end: 181
      },
      source: "A sentence removed during the latest revision.",
      text: "A sentence removed during the latest revision."
    },
    attachment: {
      state: "detached"
    },
    status: "handled",
    messages: [
      humanMessage(
        "message_detached_human",
        "Please tighten this explanation and keep the safety boundary explicit."
      ),
      agentMessage(
        "message_detached_agent",
        "Reworked the paragraph and removed the original sentence. The review is detached because its exact source no longer exists."
      )
    ]
  };
}

export function discussionState(): VisualState {
  return {
    source: richMarkdown,
    threads: [
      documentThread("thread_document", "handled", [
        humanMessage(
          "message_document_human",
          "The opening should explain why this workflow stays repository-native."
        ),
        agentMessage(
          "message_document_agent",
          "Added a concise summary of the Markdown and adjacent-sidecar ownership model."
        )
      ]),
      attachedThread("thread_evidence", "evidence before convenience", "open", [
        humanMessage(
          "message_evidence_human",
          "Could we make this principle more concrete for someone seeing the project for the first time?"
        )
      ])
    ]
  };
}

export function detachedState(): VisualState {
  return {
    source: richMarkdown,
    threads: [detachedThread()]
  };
}

export function conflictState(): VisualState {
  return {
    source: richMarkdown,
    threads: [
      documentThread("thread_conflict", "open", [
        humanMessage(
          "message_conflict",
          "Explain how a stale browser response remains safe when the sidecar changes."
        )
      ])
    ],
    targetConflict: true
  };
}

export function emptyReviewState(): VisualState {
  return {
    source: richMarkdown,
    threads: []
  };
}

export function documentErrorState(): VisualState {
  return {
    source: richMarkdown,
    threads: [],
    documentFailure: {
      status: 422,
      code: "documentInvalidUtf8",
      message: "This Markdown file is not valid UTF-8."
    }
  };
}

function targetFingerprints(threads: readonly VisualThread[]): {
  threads: Record<string, string>;
  messages: Record<string, string>;
} {
  const threadTargets: Record<string, string> = {};
  const messageTargets: Record<string, string> = {};
  for (const [threadIndex, thread] of threads.entries()) {
    threadTargets[thread.id] = String(threadIndex + 1).repeat(64);
    for (const [messageIndex, message] of thread.messages.entries()) {
      if (
        typeof message === "object" &&
        message !== null &&
        "id" in message &&
        typeof message.id === "string"
      ) {
        messageTargets[message.id] = String(((threadIndex + messageIndex + 2) % 9) + 1).repeat(64);
      }
    }
  }
  return {
    threads: threadTargets,
    messages: messageTargets
  };
}

async function fulfillJSON(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function fulfillAPI(route: Route, state: VisualState): Promise<void> {
  const request = route.request();
  const requestURL = new URL(request.url());

  if (requestURL.pathname === "/api/state") {
    if (requestURL.searchParams.has("since")) {
      await fulfillJSON(route, 200, {
        status: "unchanged",
        workspaceRevision: 4
      });
      return;
    }
    await fulfillJSON(route, 200, {
      status: "changed",
      workspaceRevision: 4,
      documentCount: 4,
      initialDocumentPath: documentPath,
      navigation: [
        {
          kind: "directory",
          name: "docs",
          path: "docs",
          children: [
            {
              kind: "document",
              name: "architecture.md",
              path: "docs/architecture.md",
              sizeBytes: 8240,
              availability: "ready",
              documentMetadataRevision: documentRevision,
              reviewMetadataRevision: null
            },
            {
              kind: "document",
              name: "reading-experience.md",
              path: documentPath,
              sizeBytes: new TextEncoder().encode(state.source).length,
              availability: "ready",
              documentMetadataRevision: documentRevision,
              reviewMetadataRevision: state.threads.length > 0 ? reviewRevision : null
            },
            {
              kind: "document",
              name: "security.md",
              path: "docs/security.md",
              sizeBytes: 4120,
              availability: "ready",
              documentMetadataRevision: documentRevision,
              reviewMetadataRevision: null
            }
          ]
        },
        {
          kind: "document",
          name: "README.md",
          path: "README.md",
          sizeBytes: 2280,
          availability: "ready",
          documentMetadataRevision: documentRevision,
          reviewMetadataRevision: null
        }
      ],
      warnings: []
    });
    return;
  }

  if (requestURL.pathname === "/api/document") {
    if (state.documentFailure) {
      await fulfillJSON(route, state.documentFailure.status, {
        error: {
          code: state.documentFailure.code,
          message: state.documentFailure.message,
          requestId: "request_m4_document_error"
        }
      });
      return;
    }
    await fulfillJSON(route, 200, {
      path: documentPath,
      revision: documentRevision,
      source: state.source
    });
    return;
  }

  if (requestURL.pathname === "/api/review") {
    if (state.reviewFailure) {
      await fulfillJSON(route, state.reviewFailure.status, {
        error: {
          code: state.reviewFailure.code,
          message: state.reviewFailure.message,
          requestId: "request_m4_review_error"
        }
      });
      return;
    }
    await fulfillJSON(route, 200, {
      path: documentPath,
      documentRevision,
      reviewRevision,
      threads: state.threads,
      targets: targetFingerprints(state.threads)
    });
    return;
  }

  if (
    state.targetConflict &&
    request.method() === "POST" &&
    requestURL.pathname.endsWith("/messages")
  ) {
    await fulfillJSON(route, 409, {
      error: {
        code: "targetChanged",
        message: "This review target changed on disk. Your change was not submitted.",
        requestId: "request_m4_conflict"
      },
      current: {
        documentRevision,
        reviewRevision: "f".repeat(64),
        targetFingerprint: null
      }
    });
    return;
  }

  await fulfillJSON(route, 404, {
    error: {
      code: "endpointNotFound",
      message: "This API endpoint does not exist.",
      requestId: "request_m4_unknown"
    }
  });
}

export async function openVisualState(
  page: Page,
  theme: VisualTheme,
  state: VisualState
): Promise<VisualSession> {
  const externalRequests: string[] = [];
  await page.addInitScript((selectedTheme: VisualTheme) => {
    window.localStorage.setItem("mdreview.theme", selectedTheme);
  }, theme);
  await page.route("**/*", async (route) => {
    const requestURL = new URL(route.request().url());
    if (requestURL.origin !== baseOrigin) {
      externalRequests.push(requestURL.href);
      await route.abort("blockedbyclient");
      return;
    }
    if (requestURL.pathname.startsWith("/api/")) {
      await fulfillAPI(route, state);
      return;
    }
    await route.continue();
  });

  await page.goto(`/`);
  await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
  await expect(page.locator(".document-panel")).toHaveAttribute("aria-busy", "false");
  await page.evaluate(async () => {
    await document.fonts.ready;
  });

  return {
    externalRequests
  };
}

export async function prepareScreenshot(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready;
    window.scrollTo(0, 0);
    for (const panel of document.querySelectorAll<HTMLElement>(".panel")) {
      panel.scrollTop = 0;
      panel.scrollLeft = 0;
    }
    const active = document.activeElement;
    if (active instanceof HTMLElement) {
      active.blur();
    }
  });
  await expect(page.locator(".app-shell")).toBeVisible();
}
