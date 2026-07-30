import { describe, expect, it } from "vitest";

import conflictContract from "./testdata/contracts/m2/conflict.json";
import createResponseContract from "./testdata/contracts/m2/create-response.json";
import createTextRequestContract from "./testdata/contracts/m2/create-text-request.json";
import reviewEmptyContract from "./testdata/contracts/m2/review-empty.json";
import reviewContract from "./testdata/contracts/m2/review.json";
import deleteResponseContract from "./testdata/contracts/m3/delete-response.json";
import deleteThreadRequestContract from "./testdata/contracts/m3/delete-thread-request.json";
import editMessageRequestContract from "./testdata/contracts/m3/edit-message-request.json";
import mutationResponseContract from "./testdata/contracts/m3/mutation-response.json";
import replyRequestContract from "./testdata/contracts/m3/reply-request.json";
import m3ReviewContract from "./testdata/contracts/m3/review.json";
import statusRequestContract from "./testdata/contracts/m3/status-request.json";
import {
  ApiClient,
  ApiProtocolError,
  ApiRequestError,
  decodeCreateThreadResponse,
  decodeDeleteThreadResponse,
  decodeDocument,
  decodeErrorEnvelope,
  decodeHealth,
  decodeMutationResponse,
  decodeReview,
  decodeWorkspaceState,
  encodeOpaqueIDSegment,
  type CreateThreadRequest,
  type ErrorEnvelope,
  type ReplyRequest,
  type StatusRequest
} from "./api";

const revision = "d0820a0afd1e1aa6b8bbf91c8f6915e6d544eec8be1c032f7779a5e6a6b7b908";

function contractFixture(name: string): unknown {
  const fixtures: Record<string, unknown> = {
    "conflict.json": conflictContract,
    "create-response.json": createResponseContract,
    "create-text-request.json": createTextRequestContract,
    "review-empty.json": reviewEmptyContract,
    "review.json": reviewContract
  };
  return fixtures[name];
}

const stateFixture = {
  status: "changed",
  workspaceRevision: 1,
  documentCount: 2,
  initialDocumentPath: "README.md",
  navigation: [
    {
      kind: "directory",
      name: "docs",
      path: "docs",
      children: [
        {
          kind: "document",
          name: "guide.md",
          path: "docs/guide.md",
          sizeBytes: 42,
          availability: "ready",
          documentMetadataRevision: "a".repeat(64),
          reviewMetadataRevision: null
        }
      ]
    },
    {
      kind: "document",
      name: "README.md",
      path: "README.md",
      sizeBytes: 12,
      availability: "ready",
      documentMetadataRevision: "b".repeat(64),
      reviewMetadataRevision: "c".repeat(64)
    }
  ],
  warnings: [
    {
      path: "vendor/.gitignore",
      code: "ignoreFileTooLarge",
      message: "This ignore file exceeds 1 MiB and was skipped."
    }
  ]
};

describe("Milestone 1 API decoding", () => {
  it("decodes the complete workspace, document, health, and error fixtures", () => {
    expect(decodeWorkspaceState(stateFixture)).toEqual(stateFixture);
    expect(
      decodeDocument({
        path: "README.md",
        revision,
        source: "# Read me\n"
      })
    ).toEqual({
      path: "README.md",
      revision,
      source: "# Read me\n"
    });
    expect(
      decodeHealth({
        root: "/home/reviewer/project",
        instanceNonce: "nonce"
      })
    ).toEqual({
      root: "/home/reviewer/project",
      instanceNonce: "nonce"
    });
    expect(
      decodeErrorEnvelope({
        error: {
          code: "documentInvalidUtf8",
          message: "This Markdown file is not valid UTF-8.",
          requestId: "request-id"
        }
      })
    ).toEqual({
      error: {
        code: "documentInvalidUtf8",
        message: "This Markdown file is not valid UTF-8.",
        requestId: "request-id"
      }
    });
  });

  it("decodes only the bounded unchanged state shape", () => {
    expect(
      decodeWorkspaceState({
        status: "unchanged",
        workspaceRevision: 7
      })
    ).toEqual({
      status: "unchanged",
      workspaceRevision: 7
    });
    expect(() =>
      decodeWorkspaceState({
        status: "unchanged",
        workspaceRevision: 7,
        navigation: []
      })
    ).toThrow(ApiProtocolError);
    expect(() =>
      decodeWorkspaceState({
        status: "unchanged",
        workspaceRevision: 7,
        future: true
      })
    ).toThrow(ApiProtocolError);
  });

  it.each([
    {
      ...stateFixture,
      workspaceRevision: 0
    },
    {
      ...stateFixture,
      initialDocumentPath: 42
    },
    {
      ...stateFixture,
      navigation: [{ kind: "document", name: "bad.md" }]
    },
    {
      ...stateFixture,
      navigation: [
        {
          kind: "document",
          name: "bad.md",
          path: "bad.md",
          sizeBytes: 1,
          availability: "unknown",
          documentMetadataRevision: revision,
          reviewMetadataRevision: null
        }
      ]
    },
    {
      ...stateFixture,
      status: "future"
    },
    {
      ...stateFixture,
      navigation: [
        {
          kind: "document",
          name: "bad.md",
          path: "bad.md",
          sizeBytes: 1,
          availability: "ready",
          documentMetadataRevision: "bad",
          reviewMetadataRevision: null
        }
      ]
    }
  ])("rejects an invalid workspace response", (value) => {
    expect(() => decodeWorkspaceState(value)).toThrow(ApiProtocolError);
  });

  it("rejects malformed revisions and unknown error codes", () => {
    expect(() =>
      decodeDocument({
        path: "README.md",
        revision: "not-a-revision",
        source: ""
      })
    ).toThrow(ApiProtocolError);
    expect(() =>
      decodeErrorEnvelope({
        error: {
          code: "futureCode",
          message: "No",
          requestId: "request-id"
        }
      })
    ).toThrow(ApiProtocolError);
  });
});

describe("ApiClient", () => {
  it("keeps read requests same-origin and credential-free", async () => {
    const requests: Array<{ url: string; authorization: string | null }> = [];
    const fetchResponse = (body: unknown): Response =>
      new Response(JSON.stringify(body), {
        headers: {
          "Content-Type": "application/json"
        }
      });
    const client = new ApiClient((input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      requests.push({
        url,
        authorization: new Headers(init?.headers).get("Authorization")
      });
      if (url === "/api/state" || url === "/api/state?since=1") {
        return Promise.resolve(fetchResponse(stateFixture));
      }
      return Promise.resolve(
        fetchResponse({
          path: "docs/guide.md",
          revision,
          source: "# Guide\n"
        })
      );
    });

    await client.getState();
    await client.getState(1);
    await client.getDocument("docs/guide.md");

    expect(requests).toEqual([
      {
        url: "/api/state",
        authorization: null
      },
      {
        url: "/api/state?since=1",
        authorization: null
      },
      {
        url: "/api/document?path=docs%2Fguide.md",
        authorization: null
      }
    ]);
  });

  it("rejects an invalid workspace revision before making a request", () => {
    const client = new ApiClient(() => {
      throw new Error("fetch must not be called");
    });

    expect(() => client.getState(0)).toThrow(TypeError);
    expect(() => client.getState(Number.MAX_SAFE_INTEGER + 1)).toThrow(TypeError);
  });

  it("decodes a structured error before exposing it to state handling", async () => {
    const envelope: ErrorEnvelope = {
      error: {
        code: "documentInvalidUtf8",
        message: "This Markdown file is not valid UTF-8.",
        requestId: "request-id"
      }
    };
    const client = new ApiClient(() =>
      Promise.resolve(
        new Response(JSON.stringify(envelope), {
          status: 422
        })
      )
    );

    await expect(client.getDocument("bad.md")).rejects.toMatchObject({
      name: "ApiRequestError",
      code: "documentInvalidUtf8",
      requestId: "request-id",
      status: 422
    });
  });

  it("rejects a successful document response for a different indexed path", async () => {
    const client = new ApiClient(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            path: "different.md",
            revision,
            source: ""
          })
        )
      )
    );

    await expect(client.getDocument("requested.md")).rejects.toBeInstanceOf(ApiProtocolError);
  });

  it("fetches contained image blobs with a same-origin credential-free request", async () => {
    const requests: Array<{
      url: string;
      authorization: string | null;
      cache: RequestCache | undefined;
      redirect: RequestRedirect | undefined;
    }> = [];
    const client = new ApiClient((input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      requests.push({
        url,
        authorization: new Headers(init?.headers).get("Authorization"),
        cache: init?.cache,
        redirect: init?.redirect
      });
      return Promise.resolve(
        new Response(new Uint8Array([0x89, 0x50, 0x4e, 0x47]), {
          headers: {
            "Content-Type": "image/png"
          }
        })
      );
    });

    const blob = await client.getAsset("docs/guide.md", "../assets/diagram.png");

    expect(blob.type).toBe("image/png");
    expect(blob.size).toBe(4);
    expect(requests).toEqual([
      {
        url: "/api/asset?documentPath=docs%2Fguide.md&reference=..%2Fassets%2Fdiagram.png",
        authorization: null,
        cache: "no-store",
        redirect: "error"
      }
    ]);
  });

  it("rejects successful asset responses outside the frozen raster contract", async () => {
    const wrongType = new ApiClient(() =>
      Promise.resolve(
        new Response("<svg/>", {
          headers: {
            "Content-Type": "image/svg+xml"
          }
        })
      )
    );

    await expect(wrongType.getAsset("README.md", "diagram.svg")).rejects.toBeInstanceOf(
      ApiProtocolError
    );
  });

  it("decodes an asset error before exposing it to the image manager", async () => {
    const client = new ApiClient(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            error: {
              code: "assetTooLarge",
              message: "This image is too large.",
              requestId: "asset-request"
            }
          }),
          {
            status: 413
          }
        )
      )
    );

    await expect(client.getAsset("README.md", "large.png")).rejects.toMatchObject({
      code: "assetTooLarge",
      requestId: "asset-request",
      status: 413
    });
  });
});

describe("Milestone 2 review API decoding", () => {
  it("decodes the shared review and creation fixtures", () => {
    expect(decodeReview(contractFixture("review.json"))).toEqual(contractFixture("review.json"));
    expect(decodeReview(contractFixture("review-empty.json"))).toEqual(
      contractFixture("review-empty.json")
    );
    expect(decodeCreateThreadResponse(contractFixture("create-response.json"))).toEqual(
      contractFixture("create-response.json")
    );
  });

  it.each(["2026-07-28T14:30:00Z", "2026-07-28T14:30:00+00:00", "2026-07-28T14:30:00-00:00"])(
    "accepts the zero-offset RFC3339 timestamp %s",
    (createdAt) => {
      const fixture = structuredClone(contractFixture("review.json")) as {
        threads: Array<{ messages: Array<{ createdAt: string }> }>;
      };
      const message = fixture.threads[0]?.messages[0];
      if (!message) {
        throw new Error("review contract has no message");
      }
      message.createdAt = createdAt;

      expect(decodeReview(fixture).threads[0]?.messages[0]?.createdAt).toBe(createdAt);
    }
  );

  it("rejects a non-UTC RFC3339 timestamp", () => {
    const fixture = structuredClone(contractFixture("review.json")) as {
      threads: Array<{ messages: Array<{ createdAt: string }> }>;
    };
    const message = fixture.threads[0]?.messages[0];
    if (!message) {
      throw new Error("review contract has no message");
    }
    message.createdAt = "2026-07-28T15:30:00+01:00";

    expect(() => decodeReview(fixture)).toThrow(ApiProtocolError);
  });

  it.each([
    {
      ...(contractFixture("review.json") as { [key: string]: unknown }),
      reviewRevision: "bad"
    },
    {
      path: "README.md",
      reviewRevision: null,
      threads: [
        {
          ...((
            contractFixture("review.json") as {
              threads: Array<{ [key: string]: unknown }>;
            }
          ).threads[0] ?? {}),
          attachment: { state: "document" }
        }
      ]
    },
    {
      path: "README.md",
      reviewRevision: null,
      threads: [
        {
          ...((
            contractFixture("review.json") as {
              threads: Array<{ [key: string]: unknown }>;
            }
          ).threads[0] ?? {}),
          anchor: {
            type: "text",
            range: { start: 10, end: 10 },
            source: "",
            text: ""
          }
        }
      ]
    }
  ])("rejects invalid review shapes", (value) => {
    expect(() => decodeReview(value)).toThrow(ApiProtocolError);
  });

  it("loads review data and sends a same-origin JSON creation request", async () => {
    const requests: Array<{
      url: string;
      method: string | undefined;
      authorization: string | null;
      contentType: string | null;
      body: string | null | undefined;
    }> = [];
    const request = contractFixture("create-text-request.json") as CreateThreadRequest;
    const client = new ApiClient((input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      requests.push({
        url,
        method: init?.method,
        authorization: new Headers(init?.headers).get("Authorization"),
        contentType: new Headers(init?.headers).get("Content-Type"),
        body: typeof init?.body === "string" ? init.body : null
      });
      const response =
        url === "/api/review?path=README.md"
          ? contractFixture("review.json")
          : contractFixture("create-response.json");
      return Promise.resolve(new Response(JSON.stringify(response)));
    });

    await client.getReview("README.md");
    await client.createThread(request);

    expect(requests).toEqual([
      {
        url: "/api/review?path=README.md",
        method: "GET",
        authorization: null,
        contentType: null,
        body: null
      },
      {
        url: "/api/threads",
        method: "POST",
        authorization: null,
        contentType: "application/json",
        body: JSON.stringify(request)
      }
    ]);
  });

  it("exposes conflict revisions without dropping the stable error", async () => {
    const client = new ApiClient(() =>
      Promise.resolve(
        new Response(JSON.stringify(contractFixture("conflict.json")), {
          status: 409
        })
      )
    );

    await expect(
      client.createThread(contractFixture("create-text-request.json") as CreateThreadRequest)
    ).rejects.toMatchObject({
      name: "ApiRequestError",
      code: "documentChanged",
      status: 409,
      current: {
        documentRevision: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
        reviewRevision: null
      }
    });
  });
});

describe("Milestone 3 review operation transport", () => {
  it("decodes whole-revision review and mutation responses", () => {
    expect(decodeReview(m3ReviewContract)).toEqual(m3ReviewContract);
    expect(decodeMutationResponse(mutationResponseContract)).toEqual(mutationResponseContract);
    expect(decodeDeleteThreadResponse(deleteResponseContract)).toEqual(deleteResponseContract);
  });

  it("rejects malformed whole-file revisions", () => {
    expect(() =>
      decodeReview({
        ...m3ReviewContract,
        reviewRevision: "not-a-revision"
      })
    ).toThrow(ApiProtocolError);
    expect(() =>
      decodeMutationResponse({
        ...mutationResponseContract,
        documentRevision: "not-a-revision"
      })
    ).toThrow(ApiProtocolError);
  });

  it.each([
    [".", "~Lg"],
    ["..", "~Li4"],
    ["thread/with/slash", "~dGhyZWFkL3dpdGgvc2xhc2g"],
    ["percent%value", "~cGVyY2VudCV2YWx1ZQ"],
    ["révision🙂", "~csOpdmlzaW9u8J-Zgg"]
  ])("encodes the opaque ID %s as one route-safe segment", (id, encoded) => {
    expect(encodeOpaqueIDSegment(id)).toBe(encoded);
    expect(encoded).not.toContain("/");
  });

  it("sends every lifecycle operation with the frozen method, route, and JSON body", async () => {
    const requests: Array<{ url: string; method: string | undefined; body: unknown }> = [];
    const client = new ApiClient((input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      requests.push({
        url,
        method: init?.method,
        body: typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : undefined
      });
      return Promise.resolve(
        new Response(
          JSON.stringify(
            init?.method === "DELETE" ? deleteResponseContract : mutationResponseContract
          )
        )
      );
    });

    await client.reply("thread/..", replyRequestContract);
    await client.editMessage(".", editMessageRequestContract);
    await client.setThreadStatus("révision🙂", statusRequestContract as StatusRequest);
    await client.deleteThread("..", deleteThreadRequestContract);

    expect(requests).toEqual([
      {
        url: "/api/threads/~dGhyZWFkLy4u/messages",
        method: "POST",
        body: replyRequestContract
      },
      {
        url: "/api/messages/~Lg",
        method: "PATCH",
        body: editMessageRequestContract
      },
      {
        url: "/api/threads/~csOpdmlzaW9u8J-Zgg/status",
        method: "PATCH",
        body: statusRequestContract
      },
      {
        url: "/api/threads/~Li4",
        method: "DELETE",
        body: deleteThreadRequestContract
      }
    ]);
  });

  it("preserves whole-file revisions from a conflict", async () => {
    const client = new ApiClient(() =>
      Promise.resolve(
        new Response(JSON.stringify(conflictContract), {
          status: 409
        })
      )
    );

    await expect(
      client.reply("thread_existing", replyRequestContract as ReplyRequest)
    ).rejects.toMatchObject({
      code: "documentChanged",
      current: {
        documentRevision: "e".repeat(64),
        reviewRevision: null
      }
    });
  });
});

export function apiRequestError(code: ErrorEnvelope["error"]["code"]): ApiRequestError {
  return new ApiRequestError(
    {
      error: {
        code,
        message: "Safe message",
        requestId: "request-id"
      }
    },
    400
  );
}
