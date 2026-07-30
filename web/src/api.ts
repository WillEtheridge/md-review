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
export type DocumentAvailability = "ready" | "tooLarge";
export type MetadataRevision = string;

export interface DirectoryNode {
  kind: "directory";
  name: string;
  path: string;
  children: NavigationNode[];
}

export interface DocumentNode {
  kind: "document";
  name: string;
  path: string;
  sizeBytes: number;
  availability: DocumentAvailability;
  documentMetadataRevision?: MetadataRevision;
  reviewMetadataRevision?: MetadataRevision | null;
}

export type NavigationNode = DirectoryNode | DocumentNode;

export interface ScanWarning {
  path: string;
  code: string;
  message: string;
}

export interface ChangedWorkspaceStateResponse {
  status: "changed";
  workspaceRevision: number;
  documentCount: number;
  initialDocumentPath: string | null;
  navigation: NavigationNode[];
  warnings: ScanWarning[];
}

export interface UnchangedWorkspaceStateResponse {
  status: "unchanged";
  workspaceRevision: number;
}

export type WorkspaceStateResponse =
  ChangedWorkspaceStateResponse | UnchangedWorkspaceStateResponse;

export interface DocumentResponse {
  path: string;
  revision: string;
  source: string;
}

export interface HealthResponse {
  root: string;
  instanceNonce: string;
}

export interface TextRange {
  start: number;
  end: number;
}

export interface TextThreadAnchor {
  type: "text";
  range: TextRange;
  source: string;
  text: string;
}

export interface DocumentThreadAnchor {
  type: "document";
}

export type ThreadAnchor = TextThreadAnchor | DocumentThreadAnchor;
export type ThreadStatus = "open" | "handled" | "resolved";

export interface ReviewMessage {
  id: string;
  author: {
    type: "human" | "agent";
    name: string;
  };
  body: string;
  createdAt: string;
  editedAt?: string;
}

interface ReviewThreadBase {
  id: string;
  status: ThreadStatus;
  messages: ReviewMessage[];
}

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

export interface DocumentReviewThread extends ReviewThreadBase {
  anchor: DocumentThreadAnchor;
  attachment: {
    state: "document";
  };
}

export type ReviewThread = TextReviewThread | DocumentReviewThread;

export interface ReviewResponse {
  path: string;
  documentRevision: string;
  reviewRevision: string | null;
  threads: ReviewThread[];
}

export interface CreateThreadRequest {
  documentPath: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string | null;
  anchor: ThreadAnchor;
  message: {
    body: string;
  };
}

export interface CreateThreadResponse {
  documentRevision: string;
  reviewRevision: string;
  thread: ReviewThread;
}

interface ReviewOperationRequest {
  documentPath: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string | null;
}

export interface ReplyRequest extends ReviewOperationRequest {
  message: {
    body: string;
  };
}

export interface StatusRequest extends ReviewOperationRequest {
  status: "open" | "resolved";
}

export type DeleteThreadRequest = ReviewOperationRequest;

export interface MutationResponse {
  documentRevision: string;
  reviewRevision: string;
  thread: ReviewThread;
}

export interface DeleteThreadResponse {
  documentRevision: string;
  reviewRevision: string;
  deletedThreadId: string;
}

export interface CurrentRevisions {
  documentRevision: string;
  reviewRevision: string | null;
}

export interface ErrorEnvelope {
  error: {
    code: ApiErrorCode;
    message: string;
  };
}

export class ApiProtocolError extends Error {
  constructor() {
    super("the server returned an invalid response");
    this.name = "ApiProtocolError";
  }
}

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

export function decodeWorkspaceState(value: unknown): WorkspaceStateResponse {
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

export function decodeDocument(value: unknown): DocumentResponse {
  const record = recordValue(value);
  return {
    path: stringValue(record.path),
    revision: revisionValue(record.revision),
    source: stringValue(record.source, true)
  };
}

export function decodeHealth(value: unknown): HealthResponse {
  const record = recordValue(value);
  return {
    root: stringValue(record.root),
    instanceNonce: stringValue(record.instanceNonce)
  };
}

const apiErrorCodes = new Set<string>(API_ERROR_CODES);

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

export function decodeCreateThreadResponse(value: unknown): CreateThreadResponse {
  return decodeThreadMutation(value);
}

export function decodeMutationResponse(value: unknown): MutationResponse {
  return decodeThreadMutation(value);
}

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

  async getReview(path: string, signal?: AbortSignal): Promise<ReviewResponse> {
    const query = new URLSearchParams({ path });
    const review = await this.#request(`/api/review?${query.toString()}`, decodeReview, { signal });
    if (review.path !== path) {
      throw new ApiProtocolError();
    }
    return review;
  }

  createThread(request: CreateThreadRequest, signal?: AbortSignal): Promise<CreateThreadResponse> {
    return this.#request("/api/threads", decodeCreateThreadResponse, {
      method: "POST",
      request,
      signal
    });
  }

  reply(threadID: string, request: ReplyRequest, signal?: AbortSignal): Promise<MutationResponse> {
    return this.#request(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}/messages`,
      decodeMutationResponse,
      { method: "POST", request, signal }
    );
  }

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

export function encodeOpaqueIDSegment(id: string): string {
  const bytes = new TextEncoder().encode(id);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return `~${btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "")}`;
}

export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}
