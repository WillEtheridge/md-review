// This module is the browser's typed boundary to the untrusted loopback API.
// All response decoders accept unknown and validate it before returning the
// transport types used by application state.

export const API_ERROR_CODES = [
  "invalidHost",
  "invalidDocumentPath",
  "invalidWorkspaceRevision",
  "invalidAssetRequest",
  "endpointNotFound",
  "documentNotFound",
  "assetNotFound",
  "methodNotAllowed",
  "documentTooLarge",
  "assetTooLarge",
  "documentInvalidUtf8",
  "invalidOrigin",
  "documentChanged",
  "reviewChanged",
  "requestTooLarge",
  "reviewTooLarge",
  "assetUnsupportedType",
  "unsupportedMediaType",
  "invalidReviewOperation",
  "reviewInvalid",
  "reviewUnsupportedSchema",
  "reviewUnsafe",
  "reviewUnavailable",
  "workspaceUnavailable",
  "documentUnavailable",
  "assetUnavailable",
  "internalError"
] as const;

export type ApiErrorCode = (typeof API_ERROR_CODES)[number];
/** Scan-time availability reported for an indexed Markdown document. */
export type DocumentAvailability = "ready" | "tooLarge";
/** Opaque SHA-256 metadata revision used to detect external changes. */
export type MetadataRevision = string;

/** Directory node in the server-owned navigation tree. */
export interface DirectoryNode {
  /** Discriminator used by recursive navigation rendering. */
  kind: "directory";
  /** Final path component displayed in the sidebar. */
  name: string;
  /** Slash-relative identity rooted at the served workspace. */
  path: string;
  /** Child directories/documents in deterministic server order. */
  children: NavigationNode[];
}

/** Markdown document node in the server-owned navigation tree. */
export interface DocumentNode {
  /** Discriminator used by document selection and metadata handling. */
  kind: "document";
  /** Final path component displayed in the sidebar. */
  name: string;
  /** Slash-relative identity sent back for document and review reads. */
  path: string;
  /** Scan-time byte size; this is not a promise that a later read succeeds. */
  sizeBytes: number;
  /** Whether the document is within the bounded read limit. */
  availability: DocumentAvailability;
  /** Scan metadata revision used to detect an external document change. */
  documentMetadataRevision?: MetadataRevision;
  /** Scan metadata revision for the adjacent sidecar, null when absent. */
  reviewMetadataRevision?: MetadataRevision | null;
}

/** Recursive navigation union returned by workspace scans. */
export type NavigationNode = DirectoryNode | DocumentNode;

/** Non-fatal scan diagnostic for an unsafe or unreadable workspace entry. */
export interface ScanWarning {
  /** Slash-relative entry that was skipped or could not be inspected. */
  path: string;
  /** Stable machine-readable warning category. */
  code: string;
  /** Human-readable explanation safe to show in the browser. */
  message: string;
}

/** Full workspace state returned when the polling revision changed. */
export interface ChangedWorkspaceStateResponse {
  status: "changed";
  /** Monotonic in-process index revision. */
  workspaceRevision: number;
  /** Number of indexed Markdown documents. */
  documentCount: number;
  /** README.md or the first discovered document, when any exists. */
  initialDocumentPath: string | null;
  /** Complete replacement navigation tree for this revision. */
  navigation: NavigationNode[];
  /** Non-fatal scan diagnostics. */
  warnings: ScanWarning[];
}

/** Cheap conditional-poll response when the workspace revision is unchanged. */
export interface UnchangedWorkspaceStateResponse {
  status: "unchanged";
  workspaceRevision: number;
}

/** Union consumed by the polling/reconciliation coordinator. */
export type WorkspaceStateResponse =
  ChangedWorkspaceStateResponse | UnchangedWorkspaceStateResponse;

/** One reopened document with its exact source-byte revision. */
export interface DocumentResponse {
  /** Indexed identity echoed to detect mismatched responses. */
  path: string;
  /** SHA-256 of exact source bytes, lower-case hexadecimal. */
  revision: string;
  /** Validated UTF-8 Markdown source. */
  source: string;
}

/** Loopback instance identity used by startup and diagnostics. */
export interface HealthResponse {
  root: string;
  instanceNonce: string;
}

/** Zero-based, half-open UTF-8 byte range in a Markdown source. */
export interface TextRange {
  /** Inclusive UTF-8 byte offset. */
  start: number;
  /** Exclusive UTF-8 byte offset. */
  end: number;
}

/** Persisted anchor for a visible text selection. */
export interface TextThreadAnchor {
  type: "text";
  /** Exact original UTF-8 source range used for attachment. */
  range: TextRange;
  /** Authored Markdown source at the time of selection. */
  source: string;
  /** Visible selection text shown above the composer/thread. */
  text: string;
}

/** Persisted anchor for a comment about the whole document. */
export interface DocumentThreadAnchor {
  type: "document";
}

/** Anchor union shared by create requests and resolved threads. */
export type ThreadAnchor = TextThreadAnchor | DocumentThreadAnchor;
/** Human/agent workflow state persisted by the sidecar. */
export type ThreadStatus = "open" | "handled" | "resolved";

/** Persisted append-only review message. */
export interface ReviewMessage {
  /** Opaque message identity used by browser keys and agent tooling. */
  id: string;
  /** Persisted author type and display name. */
  author: {
    /** Human or agent protocol identity. */
    type: "human" | "agent";
    /** Presentation name retained in the sidecar. */
    name: string;
  };
  /** Restricted Markdown body rendered in the comment panel. */
  body: string;
  /** UTC RFC3339 creation timestamp. */
  createdAt: string;
  /** Compatibility field for sidecars that contain an edit timestamp. */
  editedAt?: string;
}

interface ReviewThreadBase {
  id: string;
  status: ThreadStatus;
  messages: ReviewMessage[];
}

/** Text thread after the server calculates its current attachment. */
export interface TextReviewThread extends ReviewThreadBase {
  anchor: TextThreadAnchor;
  attachment:
    | {
        state: "attached";
        currentRange: TextRange;
      }
    | {
        state: "detached";
      };
}

/** Document-level thread; its attachment is always document-wide. */
export interface DocumentReviewThread extends ReviewThreadBase {
  anchor: DocumentThreadAnchor;
  attachment: {
    state: "document";
  };
}

/** Resolved thread union rendered by the review panel. */
export type ReviewThread = TextReviewThread | DocumentReviewThread;

/** Review snapshot tied to one exact Markdown and sidecar revision pair. */
export interface ReviewResponse {
  /** Indexed document identity echoed by the server. */
  path: string;
  /** Markdown revision against which attachments were calculated. */
  documentRevision: string;
  /** Exact sidecar revision, or null when no sidecar exists. */
  reviewRevision: string | null;
  /** Resolved persisted threads in server order. */
  threads: ReviewThread[];
}

/** Optimistic-concurrency request for creating a thread. */
export interface CreateThreadRequest {
  /** Indexed Markdown identity; the API derives the sidecar path itself. */
  documentPath: string;
  /** Revision of the Markdown the browser displayed. */
  expectedDocumentRevision: string;
  /** Revision of the displayed sidecar, null when it did not exist. */
  expectedReviewRevision: string | null;
  /** Exact document or text anchor to persist. */
  anchor: ThreadAnchor;
  message: {
    body: string;
  };
}

/** Result of creating a thread and emitting a new sidecar revision. */
export interface CreateThreadResponse {
  documentRevision: string;
  reviewRevision: string;
  thread: ReviewThread;
}

/** Common whole-document/whole-sidecar precondition for mutations. */
interface ReviewOperationRequest {
  /** Indexed Markdown identity; the server derives the sidecar path. */
  documentPath: string;
  /** Markdown revision captured when the operation editor opened. */
  expectedDocumentRevision: string;
  /** Sidecar revision captured at the same time. */
  expectedReviewRevision: string | null;
}

/** Append one message to an existing thread. */
export interface ReplyRequest extends ReviewOperationRequest {
  message: {
    body: string;
  };
}

/** Apply one browser-allowed open/resolved transition. */
export interface StatusRequest extends ReviewOperationRequest {
  status: "open" | "resolved";
}

/** Remove one unreplied thread. */
export type DeleteThreadRequest = ReviewOperationRequest;

/** Result of a reply or status mutation. */
export interface MutationResponse {
  documentRevision: string;
  reviewRevision: string;
  thread: ReviewThread;
}

/** Result of deleting a thread that no longer appears in the sidecar. */
export interface DeleteThreadResponse {
  documentRevision: string;
  reviewRevision: string;
  deletedThreadId: string;
}

/** Revisions observed by the server when it rejects a stale mutation. */
export interface CurrentRevisions {
  /** Current Markdown revision observed by the conflict response. */
  documentRevision: string;
  /** Current sidecar revision, or null when absent. */
  reviewRevision: string | null;
}

/** Stable error envelope returned by every API failure response. */
export interface ErrorEnvelope {
  error: {
    code: ApiErrorCode;
    message: string;
  };
}

/** Indicates that a response did not satisfy the documented transport shape. */
export class ApiProtocolError extends Error {
  constructor() {
    super("the server returned an invalid response");
    this.name = "ApiProtocolError";
  }
}

/** Indicates a valid API error response, including an optimistic conflict. */
export class ApiRequestError extends Error {
  readonly code: ApiErrorCode;
  readonly status: number;
  readonly current: CurrentRevisions | undefined;

  constructor(envelope: ErrorEnvelope, status: number, current?: CurrentRevisions) {
    super(envelope.error.message);
    this.name = "ApiRequestError";
    this.code = envelope.error.code;
    this.status = status;
    this.current = current;
  }
}

type FetchImplementation = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

interface JSONRequestOptions {
  method?: "POST" | "PATCH" | "DELETE";
  request?: unknown;
  signal?: AbortSignal | undefined;
}

function recordValue(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ApiProtocolError();
  }

  return value as Record<string, unknown>;
}

function stringValue(value: unknown, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) {
    throw new ApiProtocolError();
  }
  return value;
}

function integerValue(value: unknown, minimum: number): number {
  if (!Number.isSafeInteger(value) || typeof value !== "number" || value < minimum) {
    throw new ApiProtocolError();
  }
  return value;
}

function revisionValue(value: unknown): string {
  const revision = stringValue(value);
  if (!/^[0-9a-f]{64}$/u.test(revision)) {
    throw new ApiProtocolError();
  }
  return revision;
}

function nullableRevisionValue(value: unknown): string | null {
  return value === null ? null : revisionValue(value);
}

function arrayValue(value: unknown): unknown[] {
  if (!Array.isArray(value)) {
    throw new ApiProtocolError();
  }
  return value;
}

function validateNavigationNode(value: unknown): void {
  const record = recordValue(value);
  const kind = stringValue(record.kind);
  stringValue(record.name);
  stringValue(record.path);

  if (kind === "directory") {
    arrayValue(record.children).forEach(validateNavigationNode);
    return;
  }
  if (kind !== "document") {
    throw new ApiProtocolError();
  }
  if (record.availability !== "ready" && record.availability !== "tooLarge") {
    throw new ApiProtocolError();
  }
  integerValue(record.sizeBytes, 0);
}

/** Validates a changed or unchanged workspace response from unknown JSON. */
export function decodeWorkspaceState(value: unknown): WorkspaceStateResponse {
  // The unchanged variant intentionally omits navigation; callers retain their
  // last complete tree when a conditional poll reports no change.
  const record = recordValue(value);
  const workspaceRevision = integerValue(record.workspaceRevision, 1);
  if (record.status === "unchanged") {
    return { status: "unchanged", workspaceRevision };
  }
  if (record.status !== "changed") {
    throw new ApiProtocolError();
  }

  const navigation = arrayValue(record.navigation);
  navigation.forEach(validateNavigationNode);
  const warnings = arrayValue(record.warnings);
  for (const value of warnings) {
    const warning = recordValue(value);
    stringValue(warning.path);
    stringValue(warning.code);
    stringValue(warning.message);
  }
  const initialDocumentPath =
    record.initialDocumentPath === null ? null : stringValue(record.initialDocumentPath);

  return {
    status: "changed",
    workspaceRevision,
    documentCount: integerValue(record.documentCount, 0),
    initialDocumentPath,
    navigation: navigation as NavigationNode[],
    warnings: warnings as ScanWarning[]
  };
}

/** Validates one document response and its 64-hex-byte revision. */
export function decodeDocument(value: unknown): DocumentResponse {
  const record = recordValue(value);
  return {
    path: stringValue(record.path),
    revision: revisionValue(record.revision),
    source: stringValue(record.source, true)
  };
}

/** Validates the loopback health response used by startup checks. */
export function decodeHealth(value: unknown): HealthResponse {
  const record = recordValue(value);
  return {
    root: stringValue(record.root),
    instanceNonce: stringValue(record.instanceNonce)
  };
}

const apiErrorCodes = new Set<string>(API_ERROR_CODES);

/** Validates the stable error shape before constructing ApiRequestError. */
export function decodeErrorEnvelope(value: unknown): ErrorEnvelope {
  const error = recordValue(recordValue(value).error);
  const code = stringValue(error.code);
  if (!apiErrorCodes.has(code)) {
    throw new ApiProtocolError();
  }
  return {
    error: {
      code: code as ApiErrorCode,
      message: stringValue(error.message)
    }
  };
}

function decodeRange(value: unknown): TextRange {
  const record = recordValue(value);
  const start = integerValue(record.start, 0);
  const end = integerValue(record.end, 0);
  if (start >= end) {
    throw new ApiProtocolError();
  }
  return { start, end };
}

function validateAnchor(value: unknown): ThreadAnchor {
  const record = recordValue(value);
  if (record.type === "document") {
    return { type: "document" };
  }
  if (record.type !== "text") {
    throw new ApiProtocolError();
  }
  return {
    type: "text",
    range: decodeRange(record.range),
    source: stringValue(record.source),
    text: stringValue(record.text, true)
  };
}

function validateMessage(value: unknown): void {
  const record = recordValue(value);
  const author = recordValue(record.author);
  if (author.type !== "human" && author.type !== "agent") {
    throw new ApiProtocolError();
  }
  stringValue(record.id);
  stringValue(author.name);
  stringValue(record.body, true);
  stringValue(record.createdAt);
  if (record.editedAt !== undefined) {
    stringValue(record.editedAt);
  }
}

function decodeThread(value: unknown): ReviewThread {
  const record = recordValue(value);
  stringValue(record.id);
  if (record.status !== "open" && record.status !== "handled" && record.status !== "resolved") {
    throw new ApiProtocolError();
  }
  const messages = arrayValue(record.messages);
  if (messages.length === 0) {
    throw new ApiProtocolError();
  }
  messages.forEach(validateMessage);

  const anchor = validateAnchor(record.anchor);
  const attachment = recordValue(record.attachment);
  if (anchor.type === "document") {
    if (attachment.state !== "document") {
      throw new ApiProtocolError();
    }
  } else if (attachment.state === "attached") {
    decodeRange(attachment.currentRange);
  } else if (attachment.state !== "detached") {
    throw new ApiProtocolError();
  }
  return value as ReviewThread;
}

/** Validates a review snapshot, including attachment state discriminators. */
export function decodeReview(value: unknown): ReviewResponse {
  const record = recordValue(value);
  return {
    path: stringValue(record.path),
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: nullableRevisionValue(record.reviewRevision),
    threads: arrayValue(record.threads).map(decodeThread)
  };
}

function decodeThreadMutation(value: unknown): MutationResponse {
  const record = recordValue(value);
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: revisionValue(record.reviewRevision),
    thread: decodeThread(record.thread)
  };
}

/** Validates the response returned after creating a thread. */
export function decodeCreateThreadResponse(value: unknown): CreateThreadResponse {
  return decodeThreadMutation(value);
}

/** Validates the response returned after replying or changing status. */
export function decodeMutationResponse(value: unknown): MutationResponse {
  return decodeThreadMutation(value);
}

/** Validates the response returned after deleting a thread. */
export function decodeDeleteThreadResponse(value: unknown): DeleteThreadResponse {
  const record = recordValue(value);
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: revisionValue(record.reviewRevision),
    deletedThreadId: stringValue(record.deletedThreadId)
  };
}

function decodeCurrentRevisions(value: unknown): CurrentRevisions {
  const record = recordValue(value);
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: nullableRevisionValue(record.reviewRevision)
  };
}

function decodeRequestError(value: unknown): {
  envelope: ErrorEnvelope;
  current?: CurrentRevisions;
} {
  const record = recordValue(value);
  const envelope = decodeErrorEnvelope(record);
  return record.current === undefined
    ? { envelope }
    : { envelope, current: decodeCurrentRevisions(record.current) };
}

async function responseBody(response: Response): Promise<unknown> {
  try {
    return JSON.parse(await response.text()) as unknown;
  } catch {
    throw new ApiProtocolError();
  }
}

/** Typed client for the loopback API; every response is decoded at the boundary. */
export class ApiClient {
  readonly #fetch: FetchImplementation;

  constructor(fetchImplementation?: FetchImplementation) {
    this.#fetch =
      fetchImplementation ??
      ((input, init) => {
        return globalThis.fetch(input, init);
      });
  }

  async #request<TResponse>(
    endpoint: string,
    decode: (value: unknown) => TResponse,
    options: JSONRequestOptions = {}
  ): Promise<TResponse> {
    // Decode the body before interpreting status so malformed success and error
    // responses both fail as protocol errors instead of leaking unchecked data.
    const method = options.method ?? "GET";
    const hasBody = options.method !== undefined;
    const response = await this.#fetch(endpoint, {
      method,
      ...(hasBody
        ? {
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(options.request)
          }
        : {}),
      signal: options.signal ?? null
    });
    const body = await responseBody(response);
    if (!response.ok) {
      const failure = decodeRequestError(body);
      throw new ApiRequestError(failure.envelope, response.status, failure.current);
    }
    return decode(body);
  }

  /** Polls workspace metadata, optionally using a conditional revision. */
  getState(since?: number, signal?: AbortSignal): Promise<WorkspaceStateResponse> {
    if (since === undefined) {
      return this.#request("/api/state", decodeWorkspaceState, { signal });
    }
    if (!Number.isSafeInteger(since) || since < 1) {
      throw new TypeError("workspace revision must be a positive safe integer");
    }
    const query = new URLSearchParams({ since: String(since) });
    return this.#request(`/api/state?${query.toString()}`, decodeWorkspaceState, { signal });
  }

  /** Reads one indexed Markdown document and verifies the echoed identity. */
  async getDocument(path: string, signal?: AbortSignal): Promise<DocumentResponse> {
    const query = new URLSearchParams({ path });
    const document = await this.#request(`/api/document?${query.toString()}`, decodeDocument, {
      signal
    });
    if (document.path !== path) {
      throw new ApiProtocolError();
    }
    return document;
  }

  /** Reads one sidecar snapshot and verifies the echoed document identity. */
  async getReview(path: string, signal?: AbortSignal): Promise<ReviewResponse> {
    const query = new URLSearchParams({ path });
    const review = await this.#request(`/api/review?${query.toString()}`, decodeReview, { signal });
    if (review.path !== path) {
      throw new ApiProtocolError();
    }
    return review;
  }

  /** Creates a document or text thread under the supplied revision precondition. */
  createThread(request: CreateThreadRequest, signal?: AbortSignal): Promise<CreateThreadResponse> {
    return this.#request("/api/threads", decodeCreateThreadResponse, {
      method: "POST",
      request,
      signal
    });
  }

  /** Appends one message to an existing thread. */
  reply(threadID: string, request: ReplyRequest, signal?: AbortSignal): Promise<MutationResponse> {
    return this.#request(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}/messages`,
      decodeMutationResponse,
      { method: "POST", request, signal }
    );
  }

  /** Applies one open/resolved status transition. */
  setThreadStatus(
    threadID: string,
    request: StatusRequest,
    signal?: AbortSignal
  ): Promise<MutationResponse> {
    return this.#request(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}/status`,
      decodeMutationResponse,
      { method: "PATCH", request, signal }
    );
  }

  /** Deletes one unreplied thread under the supplied revision precondition. */
  deleteThread(
    threadID: string,
    request: DeleteThreadRequest,
    signal?: AbortSignal
  ): Promise<DeleteThreadResponse> {
    return this.#request(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}`,
      decodeDeleteThreadResponse,
      { method: "DELETE", request, signal }
    );
  }
}

/** Encodes an opaque UTF-8 ID as the canonical server route segment. */
export function encodeOpaqueIDSegment(id: string): string {
  // Route IDs use canonical unpadded base64url so arbitrary UTF-8 IDs stay in
  // one URL path segment and round-trip without a server-side escape ambiguity.
  const bytes = new TextEncoder().encode(id);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return `~${btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "")}`;
}

/** Identifies cancellation without treating it as a user-visible failure. */
export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}
