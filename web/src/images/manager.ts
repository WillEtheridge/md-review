export const MAX_IMAGE_ASSET_BYTES = 20 * 1024 * 1024;
export const MAX_RETAINED_IMAGE_BLOB_BYTES = 40 * 1024 * 1024;
export const MAX_CONCURRENT_IMAGE_LOADS = 4;

export const ALLOWED_IMAGE_CONTENT_TYPES = [
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp"
] as const;

export type AllowedImageContentType = (typeof ALLOWED_IMAGE_CONTENT_TYPES)[number];

export interface AssetFetcher {
  fetchAsset(documentPath: string, reference: string, signal: AbortSignal): Promise<Blob>;
}

export interface ObjectURLStore {
  createObjectURL(blob: Blob): string;
  revokeObjectURL(objectURL: string): void;
}

export type ImageResourceErrorKind =
  "missing" | "unsupported" | "oversized" | "corrupt" | "unavailable";

export type ImageResourceState =
  | {
      status: "deferred";
      reason: "initial" | "evicted" | "cancelled" | "disposed";
    }
  | {
      status: "queued";
    }
  | {
      status: "loading";
    }
  | {
      status: "ready";
      objectURL: string;
      contentType: AllowedImageContentType;
      sizeBytes: number;
    }
  | {
      status: "error";
      kind: ImageResourceErrorKind;
      retryable: boolean;
    };

export type ImageResourceListener = (state: ImageResourceState) => void;

export interface ImageResourceSubscription {
  getState(): ImageResourceState;
  setNearViewport(isNearViewport: boolean): void;
  retry(): void;
  reportDecodeFailure(objectURL: string): void;
  unsubscribe(): void;
}

export interface ImageResourceManagerOptions {
  documentPath: string;
  documentRevision: string;
  fetcher: AssetFetcher;
  objectURLs?: ObjectURLStore;
}

interface ResourceEntry {
  reference: string;
  state: ImageResourceState;
  subscribers: Map<number, ImageResourceListener>;
  nearViewportSubscribers: Set<number>;
  blockedIntersectionSubscribers: Set<number>;
  generation: number;
  controller: AbortController | undefined;
  blob: Blob | undefined;
  objectURL: string | undefined;
  lastUsed: number;
}

interface QueueEntry {
  resource: ResourceEntry;
  generation: number;
}

const allowedContentTypes = new Set<string>(ALLOWED_IMAGE_CONTENT_TYPES);

const browserObjectURLs: ObjectURLStore = {
  createObjectURL(blob) {
    return URL.createObjectURL(blob);
  },
  revokeObjectURL(objectURL) {
    URL.revokeObjectURL(objectURL);
  }
};

function allowedContentType(value: string): value is AllowedImageContentType {
  return allowedContentTypes.has(value);
}

function errorCode(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null || !("code" in error)) {
    return undefined;
  }
  const code = error.code;
  return typeof code === "string" ? code : undefined;
}

function errorName(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null || !("name" in error)) {
    return undefined;
  }
  const name = error.name;
  return typeof name === "string" ? name : undefined;
}

function failureState(error: unknown): Extract<ImageResourceState, { status: "error" }> {
  switch (errorCode(error)) {
    case "assetNotFound":
      return { status: "error", kind: "missing", retryable: true };
    case "assetUnsupportedType":
      return { status: "error", kind: "unsupported", retryable: false };
    case "assetTooLarge":
      return { status: "error", kind: "oversized", retryable: false };
    case "invalidAssetRequest":
      return { status: "error", kind: "corrupt", retryable: false };
    case "assetUnavailable":
      return { status: "error", kind: "unavailable", retryable: true };
  }
  if (errorName(error) === "ApiProtocolError") {
    return { status: "error", kind: "corrupt", retryable: false };
  }
  return { status: "error", kind: "unavailable", retryable: true };
}

function initialEntry(reference: string): ResourceEntry {
  return {
    reference,
    state: { status: "deferred", reason: "initial" },
    subscribers: new Map(),
    nearViewportSubscribers: new Set(),
    blockedIntersectionSubscribers: new Set(),
    generation: 0,
    controller: undefined,
    blob: undefined,
    objectURL: undefined,
    lastUsed: 0
  };
}

/**
 * ImageResourceManager owns every fetch, blob, and object URL for one displayed
 * document revision. Dispose it when that revision is no longer rendered.
 */
export class ImageResourceManager {
  readonly documentPath: string;
  readonly documentRevision: string;

  readonly #fetcher: AssetFetcher;
  readonly #objectURLs: ObjectURLStore;
  readonly #resources = new Map<string, ResourceEntry>();
  #queue: QueueEntry[] = [];
  #nextSubscriberID = 0;
  #usageClock = 0;
  #activeLoadCount = 0;
  #retainedBlobSizeBytes = 0;
  #disposed = false;

  constructor(options: ImageResourceManagerOptions) {
    this.documentPath = options.documentPath;
    this.documentRevision = options.documentRevision;
    this.#fetcher = options.fetcher;
    this.#objectURLs = options.objectURLs ?? browserObjectURLs;
  }

  get activeLoadCount(): number {
    return this.#activeLoadCount;
  }

  get retainedBlobSizeBytes(): number {
    return this.#retainedBlobSizeBytes;
  }

  subscribe(reference: string, listener: ImageResourceListener): ImageResourceSubscription {
    if (this.#disposed) {
      throw new Error("image resource manager is disposed");
    }
    const resource = this.#resource(reference);
    const subscriberID = ++this.#nextSubscriberID;
    resource.subscribers.set(subscriberID, listener);
    let isSubscribed = true;

    const ifSubscribed = (action: () => void): void => {
      if (isSubscribed) {
        action();
      }
    };

    return {
      getState: () => resource.state,
      setNearViewport: (isNearViewport) => {
        ifSubscribed(() => {
          this.#setNearViewport(resource, subscriberID, isNearViewport);
        });
      },
      retry: () => {
        ifSubscribed(() => {
          this.#retry(resource);
        });
      },
      reportDecodeFailure: (objectURL) => {
        ifSubscribed(() => {
          this.#reportDecodeFailure(resource, objectURL);
        });
      },
      unsubscribe: () => {
        if (!isSubscribed) {
          return;
        }
        isSubscribed = false;
        resource.subscribers.delete(subscriberID);
        resource.nearViewportSubscribers.delete(subscriberID);
        resource.blockedIntersectionSubscribers.delete(subscriberID);
      }
    };
  }

  retry(reference: string): void {
    const resource = this.#resources.get(reference);
    if (resource) {
      this.#retry(resource);
    }
  }

  reportDecodeFailure(reference: string, objectURL: string): void {
    const resource = this.#resources.get(reference);
    if (resource) {
      this.#reportDecodeFailure(resource, objectURL);
    }
  }

  dispose(): void {
    if (this.#disposed) {
      return;
    }
    this.#disposed = true;
    this.#queue = [];

    for (const resource of this.#resources.values()) {
      resource.generation += 1;
      resource.controller?.abort();
      this.#discardReadyResource(resource);
      resource.state = { status: "deferred", reason: "disposed" };
      this.#notify(resource);
      resource.subscribers.clear();
      resource.nearViewportSubscribers.clear();
      resource.blockedIntersectionSubscribers.clear();
    }
    this.#resources.clear();
  }

  #resource(reference: string): ResourceEntry {
    const existing = this.#resources.get(reference);
    if (existing) {
      return existing;
    }
    const created = initialEntry(reference);
    this.#resources.set(reference, created);
    return created;
  }

  #setNearViewport(resource: ResourceEntry, subscriberID: number, isNearViewport: boolean): void {
    if (this.#disposed) {
      return;
    }
    const wasNearViewport = resource.nearViewportSubscribers.has(subscriberID);
    if (wasNearViewport === isNearViewport) {
      return;
    }

    if (!isNearViewport) {
      resource.nearViewportSubscribers.delete(subscriberID);
      resource.blockedIntersectionSubscribers.delete(subscriberID);
      return;
    }

    resource.nearViewportSubscribers.add(subscriberID);
    if (resource.state.status === "ready") {
      this.#touch(resource);
      return;
    }
    if (resource.state.status !== "deferred") {
      return;
    }
    if (resource.blockedIntersectionSubscribers.has(subscriberID)) {
      return;
    }
    this.#queueResource(resource);
  }

  #retry(resource: ResourceEntry): void {
    if (
      this.#disposed ||
      resource.state.status === "queued" ||
      resource.state.status === "loading" ||
      resource.state.status === "ready"
    ) {
      return;
    }
    resource.blockedIntersectionSubscribers.clear();
    this.#queueResource(resource);
  }

  #reportDecodeFailure(resource: ResourceEntry, objectURL: string): void {
    if (this.#disposed || resource.state.status !== "ready" || resource.objectURL !== objectURL) {
      return;
    }
    this.#discardReadyResource(resource);
    resource.state = {
      status: "error",
      kind: "corrupt",
      retryable: false
    };
    this.#notify(resource);
  }

  #queueResource(resource: ResourceEntry): void {
    if (this.#disposed) {
      return;
    }
    resource.generation += 1;
    resource.state = { status: "queued" };
    this.#queue.push({ resource, generation: resource.generation });
    this.#notify(resource);
    this.#pumpQueue();
  }

  #pumpQueue(): void {
    while (!this.#disposed && this.#activeLoadCount < MAX_CONCURRENT_IMAGE_LOADS) {
      const queued = this.#queue.shift();
      if (!queued) {
        return;
      }
      if (
        queued.generation !== queued.resource.generation ||
        queued.resource.state.status !== "queued" ||
        queued.resource.controller
      ) {
        continue;
      }
      this.#startLoad(queued.resource, queued.generation);
    }
  }

  #startLoad(resource: ResourceEntry, generation: number): void {
    const controller = new AbortController();
    resource.controller = controller;
    resource.state = { status: "loading" };
    this.#activeLoadCount += 1;
    this.#notify(resource);
    void this.#runLoad(resource, generation, controller);
  }

  async #runLoad(
    resource: ResourceEntry,
    generation: number,
    controller: AbortController
  ): Promise<void> {
    try {
      const blob = await this.#fetcher.fetchAsset(
        this.documentPath,
        resource.reference,
        controller.signal
      );
      if (!this.#isCurrentLoad(resource, generation, controller)) {
        return;
      }
      if (!allowedContentType(blob.type)) {
        resource.state = {
          status: "error",
          kind: "corrupt",
          retryable: false
        };
        this.#notify(resource);
        return;
      }
      if (blob.size > MAX_IMAGE_ASSET_BYTES) {
        resource.state = {
          status: "error",
          kind: "oversized",
          retryable: false
        };
        this.#notify(resource);
        return;
      }
      this.#admitBlob(resource, blob, blob.type);
    } catch (error: unknown) {
      if (!this.#isCurrentLoad(resource, generation, controller)) {
        return;
      }
      if (controller.signal.aborted) {
        resource.state = { status: "deferred", reason: "cancelled" };
      } else {
        resource.state = failureState(error);
      }
      this.#notify(resource);
    } finally {
      if (resource.controller === controller) {
        resource.controller = undefined;
      }
      this.#activeLoadCount -= 1;
      this.#pumpQueue();
    }
  }

  #isCurrentLoad(
    resource: ResourceEntry,
    generation: number,
    controller: AbortController
  ): boolean {
    return (
      !this.#disposed &&
      resource.generation === generation &&
      resource.controller === controller &&
      resource.state.status === "loading"
    );
  }

  #admitBlob(resource: ResourceEntry, blob: Blob, contentType: AllowedImageContentType): void {
    while (this.#retainedBlobSizeBytes + blob.size > MAX_RETAINED_IMAGE_BLOB_BYTES) {
      const victim = this.#leastRecentlyUsedResource(resource);
      if (!victim) {
        resource.state = {
          status: "error",
          kind: "oversized",
          retryable: false
        };
        this.#notify(resource);
        return;
      }
      this.#evict(victim);
    }

    // Evict before creating the next URL so observable URL-owned bytes never
    // exceed the same 40 MiB bound as the manager's retained cache.
    const objectURL = this.#objectURLs.createObjectURL(blob);
    resource.blob = blob;
    resource.objectURL = objectURL;
    resource.lastUsed = ++this.#usageClock;
    resource.blockedIntersectionSubscribers.clear();
    resource.state = {
      status: "ready",
      objectURL,
      contentType,
      sizeBytes: blob.size
    };
    this.#retainedBlobSizeBytes += blob.size;
    this.#notify(resource);
  }

  #leastRecentlyUsedResource(excluded: ResourceEntry): ResourceEntry | undefined {
    let selected: ResourceEntry | undefined;
    for (const candidate of this.#resources.values()) {
      if (candidate === excluded || candidate.state.status !== "ready") {
        continue;
      }
      if (
        !selected ||
        candidate.lastUsed < selected.lastUsed ||
        (candidate.lastUsed === selected.lastUsed &&
          candidate.reference.localeCompare(selected.reference) < 0)
      ) {
        selected = candidate;
      }
    }
    return selected;
  }

  #evict(resource: ResourceEntry): void {
    this.#discardReadyResource(resource);
    // Subscribers already near the viewport are blocked until a genuine new
    // intersection epoch. Otherwise eviction would immediately reload the same
    // resource and churn the fixed 40 MiB budget.
    resource.blockedIntersectionSubscribers = new Set(resource.nearViewportSubscribers);
    resource.state = { status: "deferred", reason: "evicted" };
    this.#notify(resource);
  }

  #discardReadyResource(resource: ResourceEntry): void {
    if (!resource.blob || !resource.objectURL) {
      return;
    }
    const objectURL = resource.objectURL;
    this.#retainedBlobSizeBytes -= resource.blob.size;
    resource.blob = undefined;
    resource.objectURL = undefined;
    resource.lastUsed = 0;
    this.#objectURLs.revokeObjectURL(objectURL);
  }

  #touch(resource: ResourceEntry): void {
    resource.lastUsed = ++this.#usageClock;
  }

  #notify(resource: ResourceEntry): void {
    for (const listener of resource.subscribers.values()) {
      listener(resource.state);
    }
  }
}
