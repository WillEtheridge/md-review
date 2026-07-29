import { describe, expect, it } from "vitest";

import {
  MAX_CONCURRENT_IMAGE_LOADS,
  MAX_IMAGE_ASSET_BYTES,
  MAX_RETAINED_IMAGE_BLOB_BYTES,
  ImageResourceManager,
  type AssetFetcher,
  type ImageResourceState,
  type ImageResourceSubscription,
  type ObjectURLStore
} from "./manager";

interface ControlledRequest {
  documentPath: string;
  reference: string;
  signal: AbortSignal;
  resolve: (blob: Blob) => void;
  reject: (error: unknown) => void;
  settled: boolean;
}

class ControlledFetcher implements AssetFetcher {
  readonly requests: ControlledRequest[] = [];
  readonly honorAbort: boolean;
  activeCount = 0;
  maximumActiveCount = 0;

  constructor(honorAbort = true) {
    this.honorAbort = honorAbort;
  }

  fetchAsset(documentPath: string, reference: string, signal: AbortSignal): Promise<Blob> {
    this.activeCount += 1;
    this.maximumActiveCount = Math.max(this.maximumActiveCount, this.activeCount);

    let resolvePromise: (blob: Blob) => void = () => undefined;
    let rejectPromise: (error: unknown) => void = () => undefined;
    const promise = new Promise<Blob>((resolve, reject) => {
      resolvePromise = resolve;
      rejectPromise = reject;
    });
    const request: ControlledRequest = {
      documentPath,
      reference,
      signal,
      resolve: (blob) => {
        if (request.settled) {
          return;
        }
        request.settled = true;
        resolvePromise(blob);
      },
      reject: (error) => {
        if (request.settled) {
          return;
        }
        request.settled = true;
        rejectPromise(error);
      },
      settled: false
    };
    this.requests.push(request);

    if (this.honorAbort) {
      signal.addEventListener(
        "abort",
        () => {
          const error = new Error("aborted");
          error.name = "AbortError";
          request.reject(error);
        },
        { once: true }
      );
    }

    return promise.finally(() => {
      this.activeCount -= 1;
    });
  }
}

class FakeObjectURLStore implements ObjectURLStore {
  readonly created: Array<{ objectURL: string; blob: Blob }> = [];
  readonly revoked: string[] = [];
  readonly #sizes = new Map<string, number>();
  currentSizeBytes = 0;
  maximumSizeBytes = 0;

  createObjectURL(blob: Blob): string {
    const objectURL = `blob:test-${String(this.created.length + 1)}`;
    this.created.push({ objectURL, blob });
    this.#sizes.set(objectURL, blob.size);
    this.currentSizeBytes += blob.size;
    this.maximumSizeBytes = Math.max(this.maximumSizeBytes, this.currentSizeBytes);
    return objectURL;
  }

  revokeObjectURL(objectURL: string): void {
    const sizeBytes = this.#sizes.get(objectURL);
    if (sizeBytes !== undefined) {
      this.currentSizeBytes -= sizeBytes;
      this.#sizes.delete(objectURL);
    }
    this.revoked.push(objectURL);
  }
}

class CodedError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.code = code;
  }
}

function blob(sizeBytes = 1, type = "image/png"): Blob {
  return new Blob([new Uint8Array(sizeBytes)], { type });
}

function manager(
  fetcher = new ControlledFetcher(),
  objectURLs = new FakeObjectURLStore()
): {
  manager: ImageResourceManager;
  fetcher: ControlledFetcher;
  objectURLs: FakeObjectURLStore;
} {
  return {
    manager: new ImageResourceManager({
      documentPath: "docs/guide.md",
      documentRevision: "a".repeat(64),
      fetcher,
      objectURLs
    }),
    fetcher,
    objectURLs
  };
}

function subscribe(
  resourceManager: ImageResourceManager,
  reference: string
): {
  subscription: ImageResourceSubscription;
  states: ImageResourceState[];
} {
  const states: ImageResourceState[] = [];
  const subscription = resourceManager.subscribe(reference, (state) => {
    states.push(state);
  });
  return { subscription, states };
}

async function settle(): Promise<void> {
  for (let turn = 0; turn < 6; turn += 1) {
    await Promise.resolve();
  }
}

describe("ImageResourceManager", () => {
  it("loads only near-viewport resources in FIFO order with four active requests", async () => {
    const fixture = manager();
    const subscriptions = ["one.png", "two.png", "three.png", "four.png", "five.png"].map(
      (reference) => subscribe(fixture.manager, reference).subscription
    );

    expect(fixture.fetcher.requests).toHaveLength(0);
    subscriptions.forEach((subscription) => {
      subscription.setNearViewport(true);
    });

    expect(fixture.fetcher.requests.map(({ reference }) => reference)).toEqual([
      "one.png",
      "two.png",
      "three.png",
      "four.png"
    ]);
    expect(fixture.fetcher.requests[0]).toMatchObject({
      documentPath: "docs/guide.md",
      reference: "one.png"
    });
    expect(fixture.manager.activeLoadCount).toBe(MAX_CONCURRENT_IMAGE_LOADS);
    expect(fixture.fetcher.maximumActiveCount).toBe(MAX_CONCURRENT_IMAGE_LOADS);

    fixture.fetcher.requests[1]?.resolve(blob());
    await settle();

    expect(fixture.fetcher.requests.map(({ reference }) => reference)).toEqual([
      "one.png",
      "two.png",
      "three.png",
      "four.png",
      "five.png"
    ]);
    expect(fixture.fetcher.maximumActiveCount).toBe(MAX_CONCURRENT_IMAGE_LOADS);
  });

  it("coalesces duplicate references into one blob and one object URL", async () => {
    const fixture = manager();
    const first = subscribe(fixture.manager, "shared.png");
    const second = subscribe(fixture.manager, "shared.png");

    first.subscription.setNearViewport(true);
    second.subscription.setNearViewport(true);
    expect(fixture.fetcher.requests).toHaveLength(1);

    fixture.fetcher.requests[0]?.resolve(blob(12));
    await settle();

    expect(first.subscription.getState()).toEqual(second.subscription.getState());
    expect(first.subscription.getState()).toMatchObject({
      status: "ready",
      objectURL: "blob:test-1",
      sizeBytes: 12
    });
    expect(fixture.objectURLs.created).toHaveLength(1);
    expect(fixture.manager.retainedBlobSizeBytes).toBe(12);
  });

  it.each(["image/png", "image/jpeg", "image/gif", "image/webp"])(
    "accepts the exact %s response type",
    async (contentType) => {
      const fixture = manager();
      const { subscription } = subscribe(fixture.manager, "image.bin");
      subscription.setNearViewport(true);
      fixture.fetcher.requests[0]?.resolve(blob(8, contentType));
      await settle();

      expect(subscription.getState()).toEqual({
        status: "ready",
        objectURL: "blob:test-1",
        contentType,
        sizeBytes: 8
      });
    }
  );

  it("rejects an unexpected response type and a blob over the per-image limit", async () => {
    const wrongType = manager();
    const wrongTypeSubscription = subscribe(wrongType.manager, "wrong.svg").subscription;
    wrongTypeSubscription.setNearViewport(true);
    wrongType.fetcher.requests[0]?.resolve(blob(8, "image/svg+xml"));
    await settle();

    expect(wrongTypeSubscription.getState()).toEqual({
      status: "error",
      kind: "corrupt",
      retryable: false
    });
    expect(wrongType.objectURLs.created).toHaveLength(0);

    const oversized = manager();
    const oversizedSubscription = subscribe(oversized.manager, "large.png").subscription;
    oversizedSubscription.setNearViewport(true);
    oversized.fetcher.requests[0]?.resolve(blob(MAX_IMAGE_ASSET_BYTES + 1));
    await settle();

    expect(oversizedSubscription.getState()).toEqual({
      status: "error",
      kind: "oversized",
      retryable: false
    });
    expect(oversized.manager.retainedBlobSizeBytes).toBe(0);
  });

  it.each([
    ["assetNotFound", "missing", true],
    ["assetUnsupportedType", "unsupported", false],
    ["assetTooLarge", "oversized", false],
    ["invalidAssetRequest", "corrupt", false],
    ["assetUnavailable", "unavailable", true]
  ] as const)("classifies %s as %s", async (code, kind, retryable) => {
    const fixture = manager();
    const { subscription } = subscribe(fixture.manager, "failure.png");
    subscription.setNearViewport(true);
    fixture.fetcher.requests[0]?.reject(new CodedError(code));
    await settle();

    expect(subscription.getState()).toEqual({
      status: "error",
      kind,
      retryable
    });
  });

  it("distinguishes protocol corruption from a retryable transport failure", async () => {
    const protocol = manager();
    const protocolSubscription = subscribe(protocol.manager, "protocol.png").subscription;
    protocolSubscription.setNearViewport(true);
    const protocolError = new Error("invalid response");
    protocolError.name = "ApiProtocolError";
    protocol.fetcher.requests[0]?.reject(protocolError);
    await settle();
    expect(protocolSubscription.getState()).toEqual({
      status: "error",
      kind: "corrupt",
      retryable: false
    });

    const transport = manager();
    const transportSubscription = subscribe(transport.manager, "transport.png").subscription;
    transportSubscription.setNearViewport(true);
    transport.fetcher.requests[0]?.reject(new TypeError("connection closed"));
    await settle();
    expect(transportSubscription.getState()).toEqual({
      status: "error",
      kind: "unavailable",
      retryable: true
    });
  });

  it("evicts deterministic LRU blobs and waits for a new intersection epoch", async () => {
    const fixture = manager();
    const first = subscribe(fixture.manager, "first.png").subscription;
    const second = subscribe(fixture.manager, "second.png").subscription;
    const third = subscribe(fixture.manager, "third.png").subscription;

    first.setNearViewport(true);
    fixture.fetcher.requests[0]?.resolve(blob(10 * 1024 * 1024));
    await settle();
    second.setNearViewport(true);
    fixture.fetcher.requests[1]?.resolve(blob(15 * 1024 * 1024));
    await settle();

    first.setNearViewport(false);
    first.setNearViewport(true);
    third.setNearViewport(true);
    fixture.fetcher.requests[2]?.resolve(blob(20 * 1024 * 1024));
    await settle();

    expect(second.getState()).toEqual({ status: "deferred", reason: "evicted" });
    expect(fixture.objectURLs.revoked).toEqual(["blob:test-2"]);
    expect(fixture.manager.retainedBlobSizeBytes).toBe(30 * 1024 * 1024);
    expect(fixture.fetcher.requests).toHaveLength(3);

    second.setNearViewport(true);
    expect(fixture.fetcher.requests).toHaveLength(3);
    second.setNearViewport(false);
    second.setNearViewport(true);
    expect(fixture.fetcher.requests).toHaveLength(4);

    fixture.fetcher.requests[3]?.resolve(blob(15 * 1024 * 1024));
    await settle();
    expect(first.getState()).toEqual({ status: "deferred", reason: "evicted" });
    expect(fixture.objectURLs.revoked).toEqual(["blob:test-2", "blob:test-1"]);
    expect(fixture.manager.retainedBlobSizeBytes).toBe(35 * 1024 * 1024);
    expect(fixture.objectURLs.maximumSizeBytes).toBeLessThanOrEqual(MAX_RETAINED_IMAGE_BLOB_BYTES);
  });

  it("retains exactly 40 MiB without eviction", async () => {
    const fixture = manager();
    const first = subscribe(fixture.manager, "first.png").subscription;
    const second = subscribe(fixture.manager, "second.png").subscription;

    first.setNearViewport(true);
    fixture.fetcher.requests[0]?.resolve(blob(MAX_IMAGE_ASSET_BYTES));
    await settle();
    second.setNearViewport(true);
    fixture.fetcher.requests[1]?.resolve(blob(MAX_IMAGE_ASSET_BYTES));
    await settle();

    expect(fixture.manager.retainedBlobSizeBytes).toBe(MAX_RETAINED_IMAGE_BLOB_BYTES);
    expect(fixture.objectURLs.currentSizeBytes).toBe(MAX_RETAINED_IMAGE_BLOB_BYTES);
    expect(fixture.objectURLs.maximumSizeBytes).toBe(MAX_RETAINED_IMAGE_BLOB_BYTES);
    expect(fixture.objectURLs.revoked).toEqual([]);
    expect(first.getState().status).toBe("ready");
    expect(second.getState().status).toBe("ready");
  });

  it("supports explicit retry and ready-resource reload without leaking a URL", async () => {
    const fixture = manager();
    const { subscription } = subscribe(fixture.manager, "retry.png");
    subscription.setNearViewport(true);
    fixture.fetcher.requests[0]?.reject(new CodedError("assetUnavailable"));
    await settle();

    subscription.retry();
    expect(fixture.fetcher.requests).toHaveLength(2);
    fixture.fetcher.requests[1]?.resolve(blob(4));
    await settle();
    expect(subscription.getState()).toMatchObject({
      status: "ready",
      objectURL: "blob:test-1"
    });

    subscription.reload();
    expect(fixture.objectURLs.revoked).toEqual(["blob:test-1"]);
    expect(fixture.fetcher.requests).toHaveLength(3);
    fixture.fetcher.requests[2]?.resolve(blob(7));
    await settle();
    expect(subscription.getState()).toEqual({
      status: "ready",
      objectURL: "blob:test-2",
      contentType: "image/png",
      sizeBytes: 7
    });
    expect(fixture.manager.retainedBlobSizeBytes).toBe(7);
  });

  it("aborts an active request before starting its explicit reload", async () => {
    const fixture = manager();
    const { subscription } = subscribe(fixture.manager, "reload.png");
    subscription.setNearViewport(true);
    const firstRequest = fixture.fetcher.requests[0];
    expect(firstRequest?.signal.aborted).toBe(false);

    subscription.reload();
    expect(firstRequest?.signal.aborted).toBe(true);
    expect(fixture.fetcher.requests).toHaveLength(1);
    await settle();

    expect(fixture.fetcher.requests).toHaveLength(2);
    expect(fixture.fetcher.maximumActiveCount).toBe(1);
    fixture.fetcher.requests[1]?.resolve(blob(9));
    await settle();
    expect(subscription.getState()).toMatchObject({
      status: "ready",
      sizeBytes: 9
    });
  });

  it("does not admit a stale completion when an active reload cannot abort immediately", async () => {
    const fetcher = new ControlledFetcher(false);
    const fixture = manager(fetcher);
    const { subscription } = subscribe(fixture.manager, "stale.png");
    subscription.setNearViewport(true);
    subscription.reload();

    fixture.fetcher.requests[0]?.resolve(blob(5));
    await settle();
    expect(fixture.objectURLs.created).toHaveLength(0);
    expect(fixture.fetcher.requests).toHaveLength(2);

    fixture.fetcher.requests[1]?.resolve(blob(7));
    await settle();
    expect(subscription.getState()).toEqual({
      status: "ready",
      objectURL: "blob:test-1",
      contentType: "image/png",
      sizeBytes: 7
    });
    expect(fixture.manager.retainedBlobSizeBytes).toBe(7);
  });

  it("turns a matching decode failure into corrupt state and revokes once", async () => {
    const fixture = manager();
    const { subscription } = subscribe(fixture.manager, "decode.png");
    subscription.setNearViewport(true);
    fixture.fetcher.requests[0]?.resolve(blob(5));
    await settle();

    subscription.reportDecodeFailure("blob:stale");
    expect(fixture.objectURLs.revoked).toEqual([]);
    subscription.reportDecodeFailure("blob:test-1");
    subscription.reportDecodeFailure("blob:test-1");

    expect(subscription.getState()).toEqual({
      status: "error",
      kind: "corrupt",
      retryable: false
    });
    expect(fixture.objectURLs.revoked).toEqual(["blob:test-1"]);
    expect(fixture.manager.retainedBlobSizeBytes).toBe(0);
  });

  it("aborts active work, drops the queue, revokes ready URLs, and clears subscribers", async () => {
    const fixture = manager();
    const ready = subscribe(fixture.manager, "ready.png");
    ready.subscription.setNearViewport(true);
    fixture.fetcher.requests[0]?.resolve(blob(6));
    await settle();

    const activeAndQueued = ["one.png", "two.png", "three.png", "four.png", "queued.png"].map(
      (reference) => subscribe(fixture.manager, reference)
    );
    activeAndQueued.forEach(({ subscription }) => {
      subscription.setNearViewport(true);
    });
    expect(fixture.fetcher.requests).toHaveLength(5);

    fixture.manager.dispose();
    fixture.manager.dispose();
    await settle();

    expect(fixture.objectURLs.revoked).toEqual(["blob:test-1"]);
    expect(fixture.manager.retainedBlobSizeBytes).toBe(0);
    expect(fixture.manager.activeLoadCount).toBe(0);
    expect(fixture.fetcher.requests.slice(1).every(({ signal }) => signal.aborted)).toBe(true);
    expect(fixture.fetcher.requests.some(({ reference }) => reference === "queued.png")).toBe(
      false
    );
    expect(ready.subscription.getState()).toEqual({ status: "deferred", reason: "disposed" });
    for (const { subscription } of activeAndQueued) {
      expect(subscription.getState()).toEqual({ status: "deferred", reason: "disposed" });
    }
  });

  it("ignores a late successful completion after disposal", async () => {
    const fetcher = new ControlledFetcher(false);
    const fixture = manager(fetcher);
    const { subscription, states } = subscribe(fixture.manager, "late.png");
    subscription.setNearViewport(true);
    fixture.manager.dispose();

    fixture.fetcher.requests[0]?.resolve(blob(10));
    await settle();

    expect(subscription.getState()).toEqual({ status: "deferred", reason: "disposed" });
    expect(states.at(-1)).toEqual({ status: "deferred", reason: "disposed" });
    expect(fixture.objectURLs.created).toHaveLength(0);
    expect(fixture.objectURLs.revoked).toHaveLength(0);
    expect(fixture.manager.activeLoadCount).toBe(0);
  });

  it("stops notifying an unsubscribed occurrence while preserving the shared resource", async () => {
    const fixture = manager();
    const first = subscribe(fixture.manager, "shared.png");
    const second = subscribe(fixture.manager, "shared.png");
    first.subscription.setNearViewport(true);
    first.subscription.unsubscribe();
    fixture.fetcher.requests[0]?.resolve(blob(3));
    await settle();

    expect(first.states.at(-1)).toEqual({ status: "loading" });
    expect(second.subscription.getState()).toMatchObject({
      status: "ready",
      objectURL: "blob:test-1"
    });
    expect(fixture.objectURLs.created).toHaveLength(1);
  });
});
