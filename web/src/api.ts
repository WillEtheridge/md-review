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
  "targetChanged",
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

export interface TargetFingerprints {
  threads: Record<string, string>;
  messages: Record<string, string>;
}

export interface ReviewResponse {
  path: string;
  documentRevision: string;
  reviewRevision: string | null;
  threads: ReviewThread[];
  targets?: TargetFingerprints;
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
  durability: "durable" | "uncertain";
  thread: ReviewThread;
}

interface TargetOperationRequest {
  documentPath: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string | null;
  targetFingerprint: string;
}

export interface ReplyRequest extends TargetOperationRequest {
  message: {
    body: string;
  };
}

export interface EditMessageRequest extends TargetOperationRequest {
  message: {
    body: string;
  };
}

export interface StatusRequest extends TargetOperationRequest {
  status: "open" | "resolved";
}

export type DeleteThreadRequest = TargetOperationRequest;

export interface MutationResponse {
  documentRevision: string;
  reviewRevision: string;
  durability: "durable" | "uncertain";
  thread: ReviewThread;
  targets: TargetFingerprints;
}

export interface DeleteThreadResponse {
  documentRevision: string;
  reviewRevision: string;
  durability: "durable" | "uncertain";
  deletedThreadId: string;
}

export interface CurrentRevisions {
  documentRevision: string;
  reviewRevision: string | null;
  targetFingerprint?: string | null;
}

export interface ErrorEnvelope {
  error: {
    code: ApiErrorCode;
    message: string;
    requestId: string;
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
  readonly requestId: string;
  readonly status: number;
  readonly current: CurrentRevisions | undefined;

  constructor(envelope: ErrorEnvelope, status: number, current?: CurrentRevisions) {
    super(envelope.error.message);
    this.name = "ApiRequestError";
    this.code = envelope.error.code;
    this.requestId = envelope.error.requestId;
    this.status = status;
    this.current = current;
  }
}

type FetchImplementation = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

function recordValue(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ApiProtocolError();
  }

  return value as Record<string, unknown>;
}

function stringValue(value: unknown): string {
  if (typeof value !== "string") {
    throw new ApiProtocolError();
  }
  return value;
}

function nonEmptyString(value: unknown): string {
  const result = stringValue(value);
  if (result.length === 0) {
    throw new ApiProtocolError();
  }
  return result;
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

function fingerprintMap(value: unknown): Record<string, string> {
  const record = recordValue(value);
  const fingerprints: Record<string, string> = {};
  for (const [id, fingerprint] of Object.entries(record)) {
    if (id.length === 0) {
      throw new ApiProtocolError();
    }
    fingerprints[id] = revisionValue(fingerprint);
  }
  return fingerprints;
}

function decodeTargetFingerprints(value: unknown): TargetFingerprints {
  const record = recordValue(value);
  return {
    threads: fingerprintMap(record.threads),
    messages: fingerprintMap(record.messages)
  };
}

function arrayValue(value: unknown): unknown[] {
  if (!Array.isArray(value)) {
    throw new ApiProtocolError();
  }
  return value;
}

function decodeNavigationNode(value: unknown): NavigationNode {
  const record = recordValue(value);
  const kind = stringValue(record.kind);
  const name = nonEmptyString(record.name);
  const path = nonEmptyString(record.path);

  if (kind === "directory") {
    return {
      kind,
      name,
      path,
      children: arrayValue(record.children).map(decodeNavigationNode)
    };
  }

  if (kind === "document") {
    const availability = stringValue(record.availability);
    if (availability !== "ready" && availability !== "tooLarge") {
      throw new ApiProtocolError();
    }
    return {
      kind,
      name,
      path,
      sizeBytes: integerValue(record.sizeBytes, 0),
      availability,
      documentMetadataRevision: revisionValue(record.documentMetadataRevision),
      reviewMetadataRevision: nullableRevisionValue(record.reviewMetadataRevision)
    };
  }

  throw new ApiProtocolError();
}

function decodeWarning(value: unknown): ScanWarning {
  const record = recordValue(value);
  return {
    path: nonEmptyString(record.path),
    code: nonEmptyString(record.code),
    message: nonEmptyString(record.message)
  };
}

export function decodeWorkspaceState(value: unknown): WorkspaceStateResponse {
  const record = recordValue(value);
  const status = stringValue(record.status);
  const workspaceRevision = integerValue(record.workspaceRevision, 1);
  if (status === "unchanged") {
    if (Object.keys(record).some((key) => key !== "status" && key !== "workspaceRevision")) {
      throw new ApiProtocolError();
    }
    return {
      status,
      workspaceRevision
    };
  }
  if (status !== "changed") {
    throw new ApiProtocolError();
  }
  const changedKeys = new Set([
    "status",
    "workspaceRevision",
    "documentCount",
    "initialDocumentPath",
    "navigation",
    "warnings"
  ]);
  if (Object.keys(record).some((key) => !changedKeys.has(key))) {
    throw new ApiProtocolError();
  }
  const initialDocumentPathValue = record.initialDocumentPath;
  const initialDocumentPath =
    initialDocumentPathValue === null ? null : nonEmptyString(initialDocumentPathValue);

  return {
    status,
    workspaceRevision,
    documentCount: integerValue(record.documentCount, 0),
    initialDocumentPath,
    navigation: arrayValue(record.navigation).map(decodeNavigationNode),
    warnings: arrayValue(record.warnings).map(decodeWarning)
  };
}

export function decodeDocument(value: unknown): DocumentResponse {
  const record = recordValue(value);

  return {
    path: nonEmptyString(record.path),
    revision: revisionValue(record.revision),
    source: stringValue(record.source)
  };
}

export function decodeHealth(value: unknown): HealthResponse {
  const record = recordValue(value);
  return {
    root: nonEmptyString(record.root),
    instanceNonce: nonEmptyString(record.instanceNonce)
  };
}

const apiErrorCodes = new Set<string>(API_ERROR_CODES);

export function decodeErrorEnvelope(value: unknown): ErrorEnvelope {
  const record = recordValue(value);
  const error = recordValue(record.error);
  const code = stringValue(error.code);
  if (!apiErrorCodes.has(code)) {
    throw new ApiProtocolError();
  }

  return {
    error: {
      code: code as ApiErrorCode,
      message: nonEmptyString(error.message),
      requestId: nonEmptyString(error.requestId)
    }
  };
}

function decodeRange(value: unknown, allowEmpty: boolean): TextRange {
  const record = recordValue(value);
  const start = integerValue(record.start, 0);
  const end = integerValue(record.end, 0);
  if (allowEmpty ? start > end : start >= end) {
    throw new ApiProtocolError();
  }
  return { start, end };
}

function decodeAnchor(value: unknown): ThreadAnchor {
  const record = recordValue(value);
  const type = stringValue(record.type);
  if (type === "document") {
    return { type };
  }
  if (type === "text") {
    return {
      type,
      range: decodeRange(record.range, false),
      source: nonEmptyString(record.source),
      text: stringValue(record.text)
    };
  }
  throw new ApiProtocolError();
}

function timestampValue(value: unknown): string {
  const timestamp = nonEmptyString(value);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]00:00)$/u.test(timestamp)) {
    throw new ApiProtocolError();
  }
  return timestamp;
}

function decodeMessage(value: unknown): ReviewMessage {
  const record = recordValue(value);
  const author = recordValue(record.author);
  const authorType = stringValue(author.type);
  if (authorType !== "human" && authorType !== "agent") {
    throw new ApiProtocolError();
  }

  const result: ReviewMessage = {
    id: nonEmptyString(record.id),
    author: {
      type: authorType,
      name: nonEmptyString(author.name)
    },
    body: stringValue(record.body),
    createdAt: timestampValue(record.createdAt)
  };
  if (record.editedAt !== undefined) {
    result.editedAt = timestampValue(record.editedAt);
  }
  return result;
}

function decodeThread(value: unknown): ReviewThread {
  const record = recordValue(value);
  const anchor = decodeAnchor(record.anchor);
  const attachment = recordValue(record.attachment);
  const attachmentState = stringValue(attachment.state);
  const statusValue = stringValue(record.status);
  if (statusValue !== "open" && statusValue !== "handled" && statusValue !== "resolved") {
    throw new ApiProtocolError();
  }
  const status: ThreadStatus = statusValue;
  const common = {
    id: nonEmptyString(record.id),
    status,
    messages: arrayValue(record.messages).map(decodeMessage)
  };
  if (common.messages.length === 0) {
    throw new ApiProtocolError();
  }

  if (anchor.type === "document") {
    if (attachmentState !== "document") {
      throw new ApiProtocolError();
    }
    return {
      ...common,
      anchor,
      attachment: { state: "document" }
    };
  }

  if (attachmentState === "detached") {
    return {
      ...common,
      anchor,
      attachment: { state: "detached" }
    };
  }
  if (attachmentState === "attached") {
    return {
      ...common,
      anchor,
      attachment: {
        state: "attached",
        currentRange: decodeRange(attachment.currentRange, false)
      }
    };
  }
  throw new ApiProtocolError();
}

export function decodeReview(value: unknown): ReviewResponse {
  const record = recordValue(value);
  const response: ReviewResponse = {
    path: nonEmptyString(record.path),
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: nullableRevisionValue(record.reviewRevision),
    threads: arrayValue(record.threads).map(decodeThread)
  };
  // Milestone 2 responses remain valid for historical fixtures. Mutation
  // controls separately require the target fingerprint they operate on.
  if (record.targets !== undefined) {
    response.targets = decodeTargetFingerprints(record.targets);
  }
  return response;
}

export function decodeCreateThreadResponse(value: unknown): CreateThreadResponse {
  const record = recordValue(value);
  const durability = stringValue(record.durability);
  if (durability !== "durable" && durability !== "uncertain") {
    throw new ApiProtocolError();
  }
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: revisionValue(record.reviewRevision),
    durability,
    thread: decodeThread(record.thread)
  };
}

function durabilityValue(value: unknown): "durable" | "uncertain" {
  const durability = stringValue(value);
  if (durability !== "durable" && durability !== "uncertain") {
    throw new ApiProtocolError();
  }
  return durability;
}

export function decodeMutationResponse(value: unknown): MutationResponse {
  const record = recordValue(value);
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: revisionValue(record.reviewRevision),
    durability: durabilityValue(record.durability),
    thread: decodeThread(record.thread),
    targets: decodeTargetFingerprints(record.targets)
  };
}

export function decodeDeleteThreadResponse(value: unknown): DeleteThreadResponse {
  const record = recordValue(value);
  return {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: revisionValue(record.reviewRevision),
    durability: durabilityValue(record.durability),
    deletedThreadId: nonEmptyString(record.deletedThreadId)
  };
}

function decodeCurrentRevisions(value: unknown): CurrentRevisions {
  const record = recordValue(value);
  const current: CurrentRevisions = {
    documentRevision: revisionValue(record.documentRevision),
    reviewRevision: nullableRevisionValue(record.reviewRevision)
  };
  if (record.targetFingerprint !== undefined) {
    current.targetFingerprint =
      record.targetFingerprint === null ? null : revisionValue(record.targetFingerprint);
  }
  return current;
}

function decodeRequestError(value: unknown): {
  envelope: ErrorEnvelope;
  current?: CurrentRevisions;
} {
  const record = recordValue(value);
  const envelope = decodeErrorEnvelope(record);
  if (record.current === undefined) {
    return { envelope };
  }
  return {
    envelope,
    current: decodeCurrentRevisions(record.current)
  };
}

async function responseBody(response: Response): Promise<unknown> {
  const text = await response.text();
  try {
    return JSON.parse(text) as unknown;
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

  async #get<T>(endpoint: string, decode: (value: unknown) => T, signal?: AbortSignal): Promise<T> {
    const response = await this.#fetch(endpoint, {
      method: "GET",
      signal: signal ?? null
    });
    const body = await responseBody(response);

    if (!response.ok) {
      const failure = decodeRequestError(body);
      throw new ApiRequestError(failure.envelope, response.status, failure.current);
    }

    return decode(body);
  }

  async #post<TResponse>(
    endpoint: string,
    request: unknown,
    decode: (value: unknown) => TResponse,
    signal?: AbortSignal
  ): Promise<TResponse> {
    const response = await this.#fetch(endpoint, {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify(request),
      signal: signal ?? null
    });
    const body = await responseBody(response);
    if (!response.ok) {
      const failure = decodeRequestError(body);
      throw new ApiRequestError(failure.envelope, response.status, failure.current);
    }
    return decode(body);
  }

  async #mutate<TResponse>(
    endpoint: string,
    method: "POST" | "PATCH" | "DELETE",
    request: unknown,
    decode: (value: unknown) => TResponse,
    signal?: AbortSignal
  ): Promise<TResponse> {
    const response = await this.#fetch(endpoint, {
      method,
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify(request),
      signal: signal ?? null
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
      return this.#get("/api/state", decodeWorkspaceState, signal);
    }
    if (!Number.isSafeInteger(since) || since < 1) {
      throw new TypeError("workspace revision must be a positive safe integer");
    }
    const query = new URLSearchParams({ since: String(since) });
    return this.#get(`/api/state?${query.toString()}`, decodeWorkspaceState, signal);
  }

  async getDocument(path: string, signal?: AbortSignal): Promise<DocumentResponse> {
    const query = new URLSearchParams({ path });
    const document = await this.#get(`/api/document?${query.toString()}`, decodeDocument, signal);
    if (document.path !== path) {
      throw new ApiProtocolError();
    }
    return document;
  }

  async getReview(path: string, signal?: AbortSignal): Promise<ReviewResponse> {
    const query = new URLSearchParams({ path });
    const review = await this.#get(`/api/review?${query.toString()}`, decodeReview, signal);
    if (review.path !== path) {
      throw new ApiProtocolError();
    }
    return review;
  }

  createThread(request: CreateThreadRequest, signal?: AbortSignal): Promise<CreateThreadResponse> {
    return this.#post("/api/threads", request, decodeCreateThreadResponse, signal);
  }

  reply(threadID: string, request: ReplyRequest, signal?: AbortSignal): Promise<MutationResponse> {
    return this.#mutate(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}/messages`,
      "POST",
      request,
      decodeMutationResponse,
      signal
    );
  }

  editMessage(
    messageID: string,
    request: EditMessageRequest,
    signal?: AbortSignal
  ): Promise<MutationResponse> {
    return this.#mutate(
      `/api/messages/${encodeOpaqueIDSegment(messageID)}`,
      "PATCH",
      request,
      decodeMutationResponse,
      signal
    );
  }

  setThreadStatus(
    threadID: string,
    request: StatusRequest,
    signal?: AbortSignal
  ): Promise<MutationResponse> {
    return this.#mutate(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}/status`,
      "PATCH",
      request,
      decodeMutationResponse,
      signal
    );
  }

  deleteThread(
    threadID: string,
    request: DeleteThreadRequest,
    signal?: AbortSignal
  ): Promise<DeleteThreadResponse> {
    return this.#mutate(
      `/api/threads/${encodeOpaqueIDSegment(threadID)}`,
      "DELETE",
      request,
      decodeDeleteThreadResponse,
      signal
    );
  }

  async getAsset(documentPath: string, reference: string, signal?: AbortSignal): Promise<Blob> {
    const query = new URLSearchParams({ documentPath, reference });
    const response = await this.#fetch(`/api/asset?${query.toString()}`, {
      method: "GET",
      cache: "no-store",
      redirect: "error",
      signal: signal ?? null
    });
    if (!response.ok) {
      const failure = decodeRequestError(await responseBody(response));
      throw new ApiRequestError(failure.envelope, response.status, failure.current);
    }

    const contentType = response.headers.get("Content-Type");
    if (
      contentType !== "image/png" &&
      contentType !== "image/jpeg" &&
      contentType !== "image/gif" &&
      contentType !== "image/webp"
    ) {
      throw new ApiProtocolError();
    }
    const blob = await response.blob();
    if (blob.type !== contentType || blob.size > 20 * 1024 * 1024) {
      throw new ApiProtocolError();
    }
    return blob;
  }

  fetchAsset(documentPath: string, reference: string, signal: AbortSignal): Promise<Blob> {
    return this.getAsset(documentPath, reference, signal);
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
