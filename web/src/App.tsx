import { useEffect, useMemo, useRef, useState } from "preact/hooks";

import {
  ApiClient,
  ApiProtocolError,
  ApiRequestError,
  isAbortError,
  type CreateThreadRequest,
  type ChangedWorkspaceStateResponse,
  type DeleteThreadRequest,
  type DocumentNode,
  type DocumentResponse,
  type EditMessageRequest,
  type NavigationNode,
  type ReplyRequest,
  type ReviewResponse,
  type StatusRequest,
  type TextThreadAnchor
} from "./api";
import "./app.css";
import { documentFailure, type DocumentFailure } from "./document-state";
import { ImageResourceManager } from "./images/manager";
import {
  ancestorDirectoryPaths,
  documentPaths,
  filterNavigation,
  findDocument,
  orderNavigation
} from "./navigation";
import { buildRenderModel, type DocumentNavigation } from "./markdown/renderer";
import type { RenderModel } from "./markdown/types";
import {
  MAX_ANCHOR_SOURCE_BYTES,
  MAX_MESSAGE_BODY_BYTES,
  ReviewPanel,
  ReviewedDocument,
  type ReviewComposer,
  type ReviewLoad,
  type ReviewOperation
} from "./review";
import { PollCoordinator, browserPollClock, browserVisibilitySource } from "./polling";
import { applyThemeMode, browserThemeStorage, persistThemeMode, type ThemeMode } from "./theme";

type WorkspaceView =
  | {
      status: "loading";
    }
  | {
      status: "ready";
      state: ChangedWorkspaceStateResponse;
    }
  | {
      status: "error";
      reason: "unavailable";
    };

type DocumentView =
  | {
      status: "idle";
    }
  | {
      status: "loading";
      path: string;
    }
  | ({
      path: string;
    } & DocumentFailure)
  | {
      status: "ready";
      path: string;
      revision: string;
      documentMetadataRevision: string;
      reviewMetadataRevision: string | null;
      model: RenderModel;
      review: ReviewLoad;
    };

interface PendingDocumentChange {
  path: string;
  documentMetadataRevision: string | null;
}

interface AttemptedMetadata {
  path: string;
  documentMetadataRevision: string;
  reviewMetadataRevision: string | null;
}

const DOCUMENT_CHANGED_NOTICE =
  "Document changed on disk. Finish or discard your comment to reload.";

function reviewFailure(error: unknown): Extract<ReviewLoad, { status: "error" }> {
  if (error instanceof ApiRequestError) {
    if (error.code === "reviewInvalid") {
      return {
        status: "error",
        title: "The review sidecar is invalid",
        message: "Fix the adjacent review JSON before adding comments."
      };
    }
    if (error.code === "reviewUnsupportedSchema") {
      return {
        status: "error",
        title: "This review uses a newer schema",
        message: "It can be read from disk, but this version of mdReview will not change it."
      };
    }
    if (error.code === "reviewTooLarge") {
      return {
        status: "error",
        title: "The review sidecar is too large",
        message: "Review sidecars larger than 8 MiB are not loaded."
      };
    }
    if (error.code === "reviewUnsafe") {
      return {
        status: "error",
        title: "The review sidecar is unsafe",
        message: "The adjacent sidecar must be a regular contained file."
      };
    }
  }
  return {
    status: "error",
    title: "Review comments could not be loaded",
    message: "The document is still readable, but its review sidecar is unavailable."
  };
}

function readyReview(response: ReviewResponse): ReviewLoad {
  return {
    status: "ready",
    revision: response.reviewRevision,
    threads: response.threads
  };
}

function isDocumentReadFailure(error: unknown): boolean {
  return (
    error instanceof ApiRequestError &&
    (error.code === "documentNotFound" ||
      error.code === "documentTooLarge" ||
      error.code === "documentInvalidUtf8" ||
      error.code === "documentUnavailable")
  );
}

async function loadDocumentAndReview(
  api: ApiClient,
  path: string,
  signal: AbortSignal
): Promise<{ document: DocumentResponse; review: ReviewLoad }> {
  const maximumAttempts = 3;
  for (let attempt = 1; attempt <= maximumAttempts; attempt += 1) {
    const [documentResult, reviewResult] = await Promise.allSettled([
      api.getDocument(path, signal),
      api.getReview(path, signal)
    ]);
    if (documentResult.status === "rejected") {
      throw documentResult.reason;
    }
    if (reviewResult.status === "rejected") {
      return {
        document: documentResult.value,
        review: reviewFailure(reviewResult.reason)
      };
    }
    if (reviewResult.value.documentRevision === documentResult.value.revision) {
      return {
        document: documentResult.value,
        review: readyReview(reviewResult.value)
      };
    }
    if (attempt === maximumAttempts) {
      return {
        document: documentResult.value,
        review: {
          status: "error",
          title: "Review comments changed while loading",
          message:
            "mdReview could not obtain comments calculated for the displayed document. Try selecting the document again."
        }
      };
    }
  }

  throw new ApiProtocolError();
}

function createFailureMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.code === "documentChanged" || error.code === "reviewChanged") {
      return "The document or review changed on disk. Reload before trying again.";
    }
    if (error.code === "reviewTooLarge") {
      return "The review sidecar would exceed its 8 MiB limit.";
    }
    if (error.code === "reviewInvalid") {
      return "The review sidecar became invalid.";
    }
    if (error.code === "reviewUnsupportedSchema") {
      return "The review sidecar now uses an unsupported schema.";
    }
    if (error.code === "reviewUnsafe") {
      return "The review sidecar is no longer a safe regular file.";
    }
    if (error.code === "invalidReviewOperation" || error.code === "requestTooLarge") {
      return error.message;
    }
  }
  if (error instanceof ApiProtocolError) {
    return "The server returned an invalid response.";
  }
  return "The comment could not be saved.";
}

function operationFailureMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.code === "documentChanged" || error.code === "reviewChanged") {
      return "The document or review changed on disk. Reload before trying again.";
    }
    if (error.code === "reviewInvalid") {
      return "The review sidecar became invalid.";
    }
    if (error.code === "reviewUnsupportedSchema") {
      return "The review sidecar now uses an unsupported schema.";
    }
    if (error.code === "reviewUnsafe") {
      return "The review sidecar is no longer a safe regular file.";
    }
    if (
      error.code === "invalidReviewOperation" ||
      error.code === "reviewTooLarge" ||
      error.code === "requestTooLarge"
    ) {
      return error.message;
    }
  }
  if (error instanceof ApiProtocolError) {
    return "The server returned an invalid response.";
  }
  return "The review change could not be saved.";
}

function NavigationItems({
  nodes,
  currentPath,
  expandedDirectories,
  expandDirectories,
  onSelect,
  onToggleDirectory
}: {
  nodes: readonly NavigationNode[];
  currentPath: string | null;
  expandedDirectories: ReadonlySet<string>;
  expandDirectories: boolean;
  onSelect: (path: string) => void;
  onToggleDirectory: (path: string) => void;
}) {
  return (
    <ul class="navigation-list">
      {nodes.map((node) =>
        node.kind === "directory" ? (
          <li key={node.path} class="navigation-directory">
            <button
              class="directory-name"
              type="button"
              aria-expanded={expandDirectories || expandedDirectories.has(node.path)}
              onClick={() => {
                onToggleDirectory(node.path);
              }}
            >
              <span aria-hidden="true">
                {expandDirectories || expandedDirectories.has(node.path) ? "▾" : "▸"}
              </span>
              {node.name}
            </button>
            {expandDirectories || expandedDirectories.has(node.path) ? (
              <NavigationItems
                nodes={node.children}
                currentPath={currentPath}
                expandedDirectories={expandedDirectories}
                expandDirectories={expandDirectories}
                onSelect={onSelect}
                onToggleDirectory={onToggleDirectory}
              />
            ) : null}
          </li>
        ) : (
          <li key={node.path}>
            <button
              class="document-link"
              type="button"
              aria-current={currentPath === node.path ? "page" : undefined}
              data-availability={node.availability}
              title={node.path}
              onClick={() => {
                onSelect(node.path);
              }}
            >
              <span aria-hidden="true">◇</span>
              <span>{node.name}</span>
              {node.availability === "tooLarge" ? <span class="document-badge">Large</span> : null}
            </button>
          </li>
        )
      )}
    </ul>
  );
}

function DocumentMessage({
  title,
  state = "empty",
  children
}: {
  title: string;
  state?: "empty" | "error";
  children: preact.ComponentChildren;
}) {
  return (
    <section class="document-message" data-state={state} aria-labelledby="document-message-title">
      <p class="section-kicker">Document</p>
      <h2 id="document-message-title">{title}</h2>
      <div>{children}</div>
    </section>
  );
}

function decodeFragment(fragment: string): string | undefined {
  try {
    return decodeURIComponent(fragment);
  } catch {
    return undefined;
  }
}

function ThemeControl({
  mode,
  onChange
}: {
  mode: ThemeMode;
  onChange: (mode: ThemeMode) => void;
}) {
  const [systemMode, setSystemMode] = useState<"light" | "dark">(() =>
    globalThis.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"
  );

  useEffect(() => {
    if (mode !== "system") {
      return;
    }

    const mediaQuery = globalThis.matchMedia("(prefers-color-scheme: dark)");
    const updateSystemMode = (): void => {
      setSystemMode(mediaQuery.matches ? "dark" : "light");
    };

    updateSystemMode();
    mediaQuery.addEventListener("change", updateSystemMode);
    return () => {
      mediaQuery.removeEventListener("change", updateSystemMode);
    };
  }, [mode]);

  const selectedMode = mode === "system" ? systemMode : mode;

  return (
    <fieldset class="theme-control">
      <legend>Theme</legend>
      <div class="theme-options">
        {(["light", "dark"] as const).map((option) => (
          <button
            key={option}
            type="button"
            aria-label={option.charAt(0).toUpperCase() + option.slice(1)}
            aria-pressed={selectedMode === option}
            title={`${option.charAt(0).toUpperCase() + option.slice(1)} theme`}
            onClick={() => {
              onChange(option);
            }}
          >
            {option === "light" ? (
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <circle cx="12" cy="12" r="4" />
                <path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42" />
              </svg>
            ) : (
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M20.5 14.1A8.5 8.5 0 0 1 9.9 3.5a8.5 8.5 0 1 0 10.6 10.6Z" />
              </svg>
            )}
          </button>
        ))}
      </div>
    </fieldset>
  );
}

function requiredMetadata(documentNode: DocumentNode): {
  documentRevision: string;
  reviewRevision: string | null;
} {
  if (documentNode.documentMetadataRevision === undefined) {
    throw new ApiProtocolError();
  }
  return {
    documentRevision: documentNode.documentMetadataRevision,
    reviewRevision: documentNode.reviewMetadataRevision ?? null
  };
}

export function App({ initialTheme }: { initialTheme: ThemeMode }) {
  const api = useMemo(() => new ApiClient(), []);
  const [workspace, setWorkspace] = useState<WorkspaceView>({ status: "loading" });
  const [document, setDocument] = useState<DocumentView>({ status: "idle" });
  const [currentPath, setCurrentPath] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [expandedDirectories, setExpandedDirectories] = useState<ReadonlySet<string>>(
    () => new Set<string>()
  );
  const [retryNumber, setRetryNumber] = useState(0);
  const [pendingFragment, setPendingFragment] = useState<DocumentNavigation | null>(null);
  const [composer, setComposer] = useState<ReviewComposer | null>(null);
  const [operationEditorActive, setOperationEditorActive] = useState(false);
  const [pendingDocumentChange, setPendingDocumentChange] = useState<PendingDocumentChange | null>(
    null
  );
  const [pollNotice, setPollNotice] = useState<string | null>(null);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [themeMode, setThemeMode] = useState<ThemeMode>(initialTheme);
  const documentPanel = useRef<HTMLElement>(null);
  const mutationControllerRef = useRef<AbortController | null>(null);
  const navigationControllerRef = useRef<AbortController | null>(null);
  const pollCoordinatorRef = useRef<PollCoordinator | null>(null);
  const focusDocumentAfterLoadRef = useRef(false);
  const loadGenerationRef = useRef(0);
  const workspaceRef = useRef(workspace);
  const documentStateRef = useRef(document);
  const currentPathRef = useRef(currentPath);
  const composerStateRef = useRef(composer);
  const composerActiveRef = useRef(composer !== null);
  const operationEditorActiveRef = useRef(operationEditorActive);
  const pendingDocumentChangeRef = useRef(pendingDocumentChange);
  const attemptedMetadataRef = useRef<AttemptedMetadata | null>(null);
  const reconcileCycleRef = useRef<(signal: AbortSignal) => Promise<void>>(async () => {});
  const pollErrorRef = useRef<(error: unknown) => "continue" | "stop">(() => "continue");
  const displayedDocumentPath = document.status === "ready" ? document.path : null;
  const displayedDocumentRevision = document.status === "ready" ? document.revision : null;
  const imageManager = useMemo(
    () =>
      displayedDocumentPath && displayedDocumentRevision
        ? new ImageResourceManager({
            documentPath: displayedDocumentPath,
            documentRevision: displayedDocumentRevision,
            fetcher: api
          })
        : null,
    [api, displayedDocumentPath, displayedDocumentRevision]
  );

  useEffect(
    () => () => {
      imageManager?.dispose();
    },
    [imageManager]
  );

  const updateWorkspace = (next: WorkspaceView): void => {
    workspaceRef.current = next;
    setWorkspace(next);
  };

  const updateDocument = (next: DocumentView): void => {
    documentStateRef.current = next;
    setDocument(next);
  };

  const transformDocument = (transform: (current: DocumentView) => DocumentView): void => {
    const next = transform(documentStateRef.current);
    documentStateRef.current = next;
    setDocument(next);
  };

  const updateComposer = (next: ReviewComposer | null): void => {
    composerStateRef.current = next;
    composerActiveRef.current = next !== null;
    setComposer(next);
  };

  const transformComposer = (
    transform: (current: ReviewComposer | null) => ReviewComposer | null
  ): void => {
    updateComposer(transform(composerStateRef.current));
  };

  const updatePendingDocumentChange = (next: PendingDocumentChange | null): void => {
    pendingDocumentChangeRef.current = next;
    setPendingDocumentChange(next);
  };

  const expandDocumentAncestors = (path: string): void => {
    const ancestors = ancestorDirectoryPaths(path);
    if (ancestors.length === 0) {
      return;
    }
    setExpandedDirectories((current) => {
      const next = new Set(current);
      for (const ancestor of ancestors) {
        next.add(ancestor);
      }
      return next;
    });
  };

  const toggleDirectory = (path: string): void => {
    setExpandedDirectories((current) => {
      const next = new Set(current);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  const hasDraftGuard = (): boolean =>
    composerActiveRef.current || operationEditorActiveRef.current;

  const rememberAttemptedMetadata = (
    path: string,
    indexedDocument: DocumentNode
  ): AttemptedMetadata => {
    const metadata = requiredMetadata(indexedDocument);
    const attempted = {
      path,
      documentMetadataRevision: metadata.documentRevision,
      reviewMetadataRevision: metadata.reviewRevision
    };
    attemptedMetadataRef.current = attempted;
    return attempted;
  };

  const commitCoordinatedDocument = async (
    path: string,
    indexedDocument: DocumentNode,
    loadedDocument: DocumentResponse,
    loadedReview: ReviewLoad,
    signal: AbortSignal,
    generation: number
  ): Promise<void> => {
    const model = await buildRenderModel(loadedDocument.source);
    if (
      signal.aborted ||
      generation !== loadGenerationRef.current ||
      currentPathRef.current !== path
    ) {
      return;
    }
    const metadata = requiredMetadata(indexedDocument);
    updateDocument({
      status: "ready",
      path,
      revision: loadedDocument.revision,
      documentMetadataRevision: metadata.documentRevision,
      reviewMetadataRevision: metadata.reviewRevision,
      model,
      review: loadedReview
    });
    rememberAttemptedMetadata(path, indexedDocument);
    updatePendingDocumentChange(null);
  };

  const loadCoordinatedDocument = async (
    path: string,
    indexedDocument: DocumentNode,
    signal: AbortSignal,
    generation: number,
    showLoading: boolean
  ): Promise<void> => {
    if (indexedDocument.availability === "tooLarge") {
      rememberAttemptedMetadata(path, indexedDocument);
      if (generation === loadGenerationRef.current && currentPathRef.current === path) {
        updateDocument({ status: "tooLarge", path });
        updatePendingDocumentChange(null);
      }
      return;
    }
    const attemptedMetadata = rememberAttemptedMetadata(path, indexedDocument);
    if (showLoading) {
      updateDocument({ status: "loading", path });
    }
    try {
      const result = await loadDocumentAndReview(api, path, signal);
      await commitCoordinatedDocument(
        path,
        indexedDocument,
        result.document,
        result.review,
        signal,
        generation
      );
      if (
        (signal.aborted ||
          generation !== loadGenerationRef.current ||
          currentPathRef.current !== path) &&
        attemptedMetadataRef.current === attemptedMetadata
      ) {
        attemptedMetadataRef.current = null;
      }
    } catch (error: unknown) {
      if (
        isAbortError(error) ||
        signal.aborted ||
        generation !== loadGenerationRef.current ||
        currentPathRef.current !== path
      ) {
        if (attemptedMetadataRef.current === attemptedMetadata) {
          attemptedMetadataRef.current = null;
        }
        return;
      }
      updateDocument({
        path,
        ...documentFailure(error)
      });
      updatePendingDocumentChange(null);
    }
  };

  const commitKnownDocument = async (
    indexedDocument: DocumentNode,
    loadedDocument: DocumentResponse,
    signal: AbortSignal,
    generation: number
  ): Promise<void> => {
    let coordinatedDocument = loadedDocument;
    let loadedReview: ReviewLoad;
    try {
      const response = await api.getReview(loadedDocument.path, signal);
      if (response.documentRevision !== loadedDocument.revision) {
        const result = await loadDocumentAndReview(api, loadedDocument.path, signal);
        coordinatedDocument = result.document;
        loadedReview = result.review;
      } else {
        loadedReview = readyReview(response);
      }
    } catch (error: unknown) {
      if (isAbortError(error)) {
        throw error;
      }
      loadedReview = reviewFailure(error);
    }
    await commitCoordinatedDocument(
      loadedDocument.path,
      indexedDocument,
      coordinatedDocument,
      loadedReview,
      signal,
      generation
    );
  };

  const freezeDocument = (path: string, indexedDocument?: DocumentNode): void => {
    updatePendingDocumentChange({
      path,
      documentMetadataRevision: indexedDocument?.documentMetadataRevision ?? null
    });
  };

  const applyReviewRefresh = async (
    indexedDocument: DocumentNode,
    current: Extract<DocumentView, { status: "ready" }>,
    signal: AbortSignal,
    generation: number
  ): Promise<void> => {
    const metadata = requiredMetadata(indexedDocument);
    rememberAttemptedMetadata(current.path, indexedDocument);
    let response: ReviewResponse;
    try {
      response = await api.getReview(current.path, signal);
    } catch (error: unknown) {
      if (isAbortError(error)) {
        throw error;
      }
      if (isDocumentReadFailure(error)) {
        if (hasDraftGuard()) {
          freezeDocument(current.path, indexedDocument);
          return;
        }
        updateDocument({
          path: current.path,
          ...documentFailure(error)
        });
        updatePendingDocumentChange(null);
        return;
      }
      const review = reviewFailure(error);
      if (
        signal.aborted ||
        generation !== loadGenerationRef.current ||
        currentPathRef.current !== current.path
      ) {
        return;
      }
      updateDocument({
        ...current,
        documentMetadataRevision: metadata.documentRevision,
        reviewMetadataRevision: metadata.reviewRevision,
        review
      });
      return;
    }

    if (response.documentRevision !== current.revision) {
      try {
        const latestDocument = await api.getDocument(current.path, signal);
        if (latestDocument.revision !== current.revision && hasDraftGuard()) {
          freezeDocument(current.path, indexedDocument);
          return;
        }
        await commitKnownDocument(indexedDocument, latestDocument, signal, generation);
        return;
      } catch (error: unknown) {
        if (isAbortError(error)) {
          throw error;
        }
        if (hasDraftGuard()) {
          freezeDocument(current.path, indexedDocument);
          return;
        }
        updateDocument({
          path: current.path,
          ...documentFailure(error)
        });
        updatePendingDocumentChange(null);
        return;
      }
    }
    const review = readyReview(response);

    if (
      signal.aborted ||
      generation !== loadGenerationRef.current ||
      currentPathRef.current !== current.path
    ) {
      return;
    }
    updateDocument({
      ...current,
      documentMetadataRevision: metadata.documentRevision,
      reviewMetadataRevision: metadata.reviewRevision,
      review
    });
    rememberAttemptedMetadata(current.path, indexedDocument);
  };

  const reconcileActiveDocument = async (
    state: ChangedWorkspaceStateResponse,
    signal: AbortSignal
  ): Promise<void> => {
    const path = currentPathRef.current;
    if (path === null) {
      return;
    }
    const generation = loadGenerationRef.current;
    const indexedDocument = findDocument(state.navigation, path);
    const current = documentStateRef.current;

    if (!indexedDocument || indexedDocument.availability === "tooLarge") {
      if (current.status === "ready" && current.path === path && hasDraftGuard()) {
        freezeDocument(path, indexedDocument);
        return;
      }
      if (
        "path" in current &&
        current.path === path &&
        ((!indexedDocument && current.status === "removed") ||
          (indexedDocument?.availability === "tooLarge" && current.status === "tooLarge"))
      ) {
        return;
      }
      updatePendingDocumentChange(null);
      if (!indexedDocument) {
        attemptedMetadataRef.current = null;
      } else {
        rememberAttemptedMetadata(path, indexedDocument);
      }
      updateDocument({
        status: indexedDocument ? "tooLarge" : "removed",
        path
      });
      return;
    }

    if (current.status !== "ready" || current.path !== path) {
      const metadata = requiredMetadata(indexedDocument);
      const attempted = attemptedMetadataRef.current;
      if (
        "path" in current &&
        current.path === path &&
        attempted?.path === path &&
        attempted.documentMetadataRevision === metadata.documentRevision &&
        attempted.reviewMetadataRevision === metadata.reviewRevision
      ) {
        return;
      }
      await loadCoordinatedDocument(path, indexedDocument, signal, generation, true);
      return;
    }

    const metadata = requiredMetadata(indexedDocument);
    const documentMetadataChanged = current.documentMetadataRevision !== metadata.documentRevision;
    const reviewMetadataChanged = current.reviewMetadataRevision !== metadata.reviewRevision;
    if (!documentMetadataChanged && !reviewMetadataChanged) {
      if (pendingDocumentChangeRef.current?.path === path) {
        updatePendingDocumentChange(null);
      }
      return;
    }

    if (
      documentMetadataChanged &&
      hasDraftGuard() &&
      pendingDocumentChangeRef.current?.path === path &&
      pendingDocumentChangeRef.current.documentMetadataRevision === metadata.documentRevision
    ) {
      return;
    }

    if (documentMetadataChanged) {
      rememberAttemptedMetadata(path, indexedDocument);
      let latestDocument: DocumentResponse;
      try {
        latestDocument = await api.getDocument(path, signal);
      } catch (error: unknown) {
        if (isAbortError(error)) {
          throw error;
        }
        if (hasDraftGuard()) {
          freezeDocument(path, indexedDocument);
          return;
        }
        updateDocument({
          path,
          ...documentFailure(error)
        });
        updatePendingDocumentChange(null);
        return;
      }

      if (latestDocument.revision !== current.revision) {
        if (hasDraftGuard()) {
          freezeDocument(path, indexedDocument);
          return;
        }
        await commitKnownDocument(indexedDocument, latestDocument, signal, generation);
        return;
      }

      if (
        !signal.aborted &&
        generation === loadGenerationRef.current &&
        currentPathRef.current === path
      ) {
        const metadataOnlyDocument = {
          ...current,
          documentMetadataRevision: metadata.documentRevision
        };
        updateDocument(metadataOnlyDocument);
        rememberAttemptedMetadata(path, indexedDocument);
        updatePendingDocumentChange(null);
        if (reviewMetadataChanged) {
          await applyReviewRefresh(indexedDocument, metadataOnlyDocument, signal, generation);
        }
      }
      return;
    }

    await applyReviewRefresh(indexedDocument, current, signal, generation);
  };

  const reconcileCycle = async (signal: AbortSignal): Promise<void> => {
    const previousWorkspace = workspaceRef.current;
    const since =
      previousWorkspace.status === "ready" ? previousWorkspace.state.workspaceRevision : undefined;
    const response = await api.getState(since, signal);
    let state: ChangedWorkspaceStateResponse;
    if (response.status === "changed") {
      state = {
        ...response,
        navigation: orderNavigation(response.navigation)
      };
      updateWorkspace({ status: "ready", state });
      if (previousWorkspace.status !== "ready") {
        const path = state.initialDocumentPath;
        currentPathRef.current = path;
        setCurrentPath(path);
        if (path !== null) {
          expandDocumentAncestors(path);
        }
        attemptedMetadataRef.current = null;
        updateDocument({ status: "idle" });
        if (path !== null) {
          loadGenerationRef.current += 1;
        }
      }
    } else {
      if (
        previousWorkspace.status !== "ready" ||
        response.workspaceRevision !== previousWorkspace.state.workspaceRevision
      ) {
        throw new ApiProtocolError();
      }
      state = previousWorkspace.state;
    }
    setPollNotice(null);
    await reconcileActiveDocument(state, signal);
  };

  const handlePollError = (error: unknown): "continue" | "stop" => {
    if (isAbortError(error)) {
      return "continue";
    }
    if (workspaceRef.current.status === "ready") {
      setPollNotice("Workspace changes could not be checked. mdReview will try again.");
    } else {
      updateWorkspace({ status: "error", reason: "unavailable" });
    }
    return "continue";
  };

  useEffect(() => {
    reconcileCycleRef.current = reconcileCycle;
    pollErrorRef.current = handlePollError;
  });

  useEffect(() => {
    if (
      !focusDocumentAfterLoadRef.current ||
      !("path" in document) ||
      document.status === "loading"
    ) {
      return;
    }
    focusDocumentAfterLoadRef.current = false;
    const frame = requestAnimationFrame(() => {
      documentPanel.current?.focus({ preventScroll: true });
    });
    return () => {
      cancelAnimationFrame(frame);
    };
  }, [document]);

  useEffect(() => {
    if (workspaceRef.current.status !== "ready") {
      updateWorkspace({ status: "loading" });
    }
    const coordinator = new PollCoordinator({
      clock: browserPollClock(),
      visibility: browserVisibilitySource(globalThis.document),
      run: (signal) => reconcileCycleRef.current(signal),
      onError: (error) => pollErrorRef.current(error)
    });
    pollCoordinatorRef.current = coordinator;
    coordinator.start();
    return () => {
      coordinator.stop();
      if (pollCoordinatorRef.current === coordinator) {
        pollCoordinatorRef.current = null;
      }
    };
  }, [api, retryNumber]);

  useEffect(
    () => () => {
      mutationControllerRef.current?.abort();
      navigationControllerRef.current?.abort();
    },
    []
  );

  useEffect(() => {
    if (
      document.status !== "ready" ||
      !pendingFragment ||
      pendingFragment.path !== document.path ||
      pendingFragment.fragment === null
    ) {
      return;
    }

    const fragment = decodeFragment(pendingFragment.fragment);
    if (!fragment) {
      return;
    }
    const frame = requestAnimationFrame(() => {
      const target = documentPanel.current?.querySelector<HTMLElement>(
        `[id="${CSS.escape(fragment)}"]`
      );
      target?.scrollIntoView({ block: "start" });
    });
    return () => {
      cancelAnimationFrame(frame);
    };
  }, [document, pendingFragment]);

  const indexedPaths = useMemo(
    () =>
      workspace.status === "ready" ? documentPaths(workspace.state.navigation) : new Set<string>(),
    [workspace]
  );
  const filteredNavigation = useMemo(
    () =>
      workspace.status === "ready" ? filterNavigation(workspace.state.navigation, filter) : [],
    [filter, workspace]
  );

  const expectDocumentFocus = (path: string): void => {
    if ("path" in document && document.path === path && document.status !== "loading") {
      focusDocumentAfterLoadRef.current = false;
      requestAnimationFrame(() => {
        documentPanel.current?.focus({ preventScroll: true });
      });
      return;
    }
    focusDocumentAfterLoadRef.current = true;
  };

  const selectAndLoadDocument = (path: string): void => {
    navigationControllerRef.current?.abort();
    const generation = loadGenerationRef.current + 1;
    loadGenerationRef.current = generation;
    currentPathRef.current = path;
    setCurrentPath(path);
    expandDocumentAncestors(path);
    attemptedMetadataRef.current = null;
    updatePendingDocumentChange(null);

    const currentWorkspace = workspaceRef.current;
    const indexedDocument =
      currentWorkspace.status === "ready"
        ? findDocument(currentWorkspace.state.navigation, path)
        : undefined;
    if (!indexedDocument) {
      updateDocument({ status: "removed", path });
      return;
    }

    const controller = new AbortController();
    navigationControllerRef.current = controller;
    void loadCoordinatedDocument(
      path,
      indexedDocument,
      controller.signal,
      generation,
      true
    ).finally(() => {
      if (navigationControllerRef.current === controller) {
        navigationControllerRef.current = null;
      }
    });
  };

  const handleSelectDocument = (path: string): void => {
    mutationControllerRef.current?.abort();
    mutationControllerRef.current = null;
    setPendingFragment(null);
    updateComposer(null);
    operationEditorActiveRef.current = false;
    setOperationEditorActive(false);
    setActiveThreadId(null);
    expectDocumentFocus(path);
    selectAndLoadDocument(path);
  };

  const handleMarkdownNavigate = (destination: DocumentNavigation): void => {
    mutationControllerRef.current?.abort();
    mutationControllerRef.current = null;
    updateComposer(null);
    operationEditorActiveRef.current = false;
    setOperationEditorActive(false);
    setActiveThreadId(null);
    setPendingFragment(destination);
    expectDocumentFocus(destination.path);
    selectAndLoadDocument(destination.path);
  };

  const handleThemeChange = (mode: ThemeMode): void => {
    applyThemeMode(globalThis.document.documentElement, mode);
    persistThemeMode(browserThemeStorage(), mode);
    setThemeMode(mode);
  };

  const handleStartTextComment = (anchor: TextThreadAnchor): void => {
    updateComposer({
      kind: "text",
      anchor,
      draft: "",
      submitting: false,
      error: null,
      conflict: null
    });
  };

  const handleStartDocumentComment = (): void => {
    updateComposer({
      kind: "document",
      draft: "",
      submitting: false,
      error: null,
      conflict: null
    });
  };

  const handleCancelComposer = (): void => {
    mutationControllerRef.current?.abort();
    mutationControllerRef.current = null;
    updateComposer(null);
    window.getSelection()?.removeAllRanges();
    pollCoordinatorRef.current?.requestNow();
  };

  const handleSubmitComment = (): void => {
    if (
      !composer ||
      composer.submitting ||
      document.status !== "ready" ||
      document.review.status !== "ready" ||
      composer.draft.trim().length === 0 ||
      new TextEncoder().encode(composer.draft).length > MAX_MESSAGE_BODY_BYTES ||
      (composer.kind === "text" &&
        (!composer.anchor ||
          composer.anchor.range.end - composer.anchor.range.start > MAX_ANCHOR_SOURCE_BYTES))
    ) {
      return;
    }

    const request: CreateThreadRequest = {
      documentPath: document.path,
      expectedDocumentRevision: document.revision,
      expectedReviewRevision: document.review.revision,
      anchor: composer.kind === "text" && composer.anchor ? composer.anchor : { type: "document" },
      message: {
        body: composer.draft
      }
    };
    const submittedKind = composer.kind;
    const submittedPath = document.path;
    const previousDocumentRevision = document.revision;
    mutationControllerRef.current?.abort();
    const controller = new AbortController();
    mutationControllerRef.current = controller;
    transformComposer((current) =>
      current
        ? {
            ...current,
            submitting: true,
            error: null
          }
        : current
    );

    void api
      .createThread(request, controller.signal)
      .then((response) => {
        if (controller.signal.aborted) {
          return;
        }
        if (response.thread.anchor.type !== submittedKind) {
          throw new ApiProtocolError();
        }
        mutationControllerRef.current = null;
        transformDocument((current) => {
          if (
            current.status !== "ready" ||
            current.path !== submittedPath ||
            current.review.status !== "ready" ||
            response.documentRevision !== previousDocumentRevision
          ) {
            return current;
          }
          return {
            ...current,
            review: {
              status: "ready",
              revision: response.reviewRevision,
              threads: [...current.review.threads, response.thread]
            }
          };
        });
        updateComposer(null);
        setActiveThreadId(response.thread.id);
        window.getSelection()?.removeAllRanges();
      })
      .catch((error: unknown) => {
        if (isAbortError(error)) {
          return;
        }
        mutationControllerRef.current = null;
        transformComposer((current) =>
          current
            ? {
                ...current,
                submitting: false,
                error: createFailureMessage(error),
                conflict:
                  error instanceof ApiRequestError && error.status === 409
                    ? (error.current ?? current.conflict)
                    : current.conflict
              }
            : current
        );
      });
  };

  const handleReviewOperation = async (operation: ReviewOperation): Promise<void> => {
    if (document.status !== "ready" || document.review.status !== "ready") {
      throw new Error("The review is not ready. Select the document again.");
    }

    const submittedPath = document.path;
    const submittedDocumentRevision = operation.expectedDocumentRevision;
    const submittedReviewRevision = operation.expectedReviewRevision;
    const commonRequest = {
      documentPath: document.path,
      expectedDocumentRevision: submittedDocumentRevision,
      expectedReviewRevision: submittedReviewRevision
    };

    mutationControllerRef.current?.abort();
    const controller = new AbortController();
    mutationControllerRef.current = controller;

    try {
      if (operation.kind === "delete") {
        const request: DeleteThreadRequest = commonRequest;
        const response = await api.deleteThread(operation.threadId, request, controller.signal);
        if (response.deletedThreadId !== operation.threadId) {
          throw new ApiProtocolError();
        }
        transformDocument((current) => {
          if (
            current.status !== "ready" ||
            current.path !== submittedPath ||
            current.review.status !== "ready" ||
            current.revision !== submittedDocumentRevision ||
            current.review.revision !== submittedReviewRevision ||
            response.documentRevision !== submittedDocumentRevision
          ) {
            return current;
          }
          return {
            ...current,
            review: {
              status: "ready",
              revision: response.reviewRevision,
              threads: current.review.threads.filter((thread) => thread.id !== operation.threadId)
            }
          };
        });
        setActiveThreadId((current) => (current === operation.threadId ? null : current));
        if (response.documentRevision !== submittedDocumentRevision) {
          pollCoordinatorRef.current?.requestNow();
        }
        return;
      }

      const response =
        operation.kind === "reply"
          ? await api.reply(
              operation.threadId,
              {
                ...commonRequest,
                message: { body: operation.body }
              } satisfies ReplyRequest,
              controller.signal
            )
          : operation.kind === "edit"
            ? await api.editMessage(
                operation.messageId,
                {
                  ...commonRequest,
                  message: { body: operation.body }
                } satisfies EditMessageRequest,
                controller.signal
              )
            : await api.setThreadStatus(
                operation.threadId,
                {
                  ...commonRequest,
                  status: operation.status
                } satisfies StatusRequest,
                controller.signal
              );

      if (
        response.thread.id !== operation.threadId ||
        (operation.kind === "edit" &&
          !response.thread.messages.some((message) => message.id === operation.messageId))
      ) {
        throw new ApiProtocolError();
      }
      transformDocument((current) => {
        if (
          current.status !== "ready" ||
          current.path !== submittedPath ||
          current.review.status !== "ready" ||
          current.revision !== submittedDocumentRevision ||
          current.review.revision !== submittedReviewRevision ||
          response.documentRevision !== submittedDocumentRevision
        ) {
          return current;
        }
        const threadIndex = current.review.threads.findIndex(
          (thread) => thread.id === operation.threadId
        );
        if (threadIndex < 0) {
          return current;
        }
        const nextThreads = [...current.review.threads];
        nextThreads[threadIndex] = response.thread;
        return {
          ...current,
          review: {
            status: "ready",
            revision: response.reviewRevision,
            threads: nextThreads
          }
        };
      });
      setActiveThreadId(operation.threadId);
      if (response.documentRevision !== submittedDocumentRevision) {
        pollCoordinatorRef.current?.requestNow();
      }
    } catch (error: unknown) {
      if (isAbortError(error)) {
        throw error;
      }
      if (
        error instanceof ApiRequestError &&
        (error.code === "documentChanged" || error.code === "reviewChanged")
      ) {
        pollCoordinatorRef.current?.requestNow();
      }
      throw new Error(operationFailureMessage(error), { cause: error });
    } finally {
      if (mutationControllerRef.current === controller) {
        mutationControllerRef.current = null;
      }
    }
  };

  return (
    <div class="app-shell">
      <a class="skip-link" href="#document-panel">
        Skip to document
      </a>

      <aside class="panel files-panel" aria-label="Files">
        <div class="files-content">
          {workspace.status === "loading" ? (
            <p class="panel-status" role="status">
              Scanning Markdown files…
            </p>
          ) : null}

          {pollNotice ? (
            <p class="panel-status" role="status">
              {pollNotice}
            </p>
          ) : null}

          {workspace.status === "error" ? (
            <section class="panel-error" aria-labelledby="workspace-error-heading">
              <h3 id="workspace-error-heading">Workspace unavailable</h3>
              <p>Check that the workspace is still available, then try again.</p>
              <button
                type="button"
                onClick={() => {
                  setRetryNumber((value) => value + 1);
                }}
              >
                Try again
              </button>
            </section>
          ) : null}

          {workspace.status === "ready" && workspace.state.documentCount === 0 ? (
            <p class="panel-status">No Markdown files were found in this workspace.</p>
          ) : null}

          {workspace.status === "ready" && workspace.state.documentCount > 0 ? (
            filteredNavigation.length > 0 ? (
              <nav aria-label="Markdown files">
                <NavigationItems
                  nodes={filteredNavigation}
                  currentPath={currentPath}
                  expandedDirectories={expandedDirectories}
                  expandDirectories={filter.trim().length > 0}
                  onSelect={handleSelectDocument}
                  onToggleDirectory={toggleDirectory}
                />
              </nav>
            ) : (
              <p class="panel-status">No Markdown filenames match this filter.</p>
            )
          ) : null}

          {workspace.status === "ready" && workspace.state.warnings.length > 0 ? (
            <section class="scan-warnings" aria-labelledby="scan-warnings-heading">
              <h3 id="scan-warnings-heading">Scan warnings</h3>
              <ul>
                {workspace.state.warnings.map((warning) => (
                  <li key={`${warning.code}:${warning.path}`}>
                    <strong>{warning.path}</strong>
                    <span>{warning.message}</span>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </div>

        {workspace.status === "ready" && workspace.state.documentCount > 0 ? (
          <div class="filter-control">
            <input
              id="filename-filter"
              type="search"
              aria-label="Filter filenames"
              value={filter}
              placeholder="Filter files"
              onInput={(event) => {
                setFilter(event.currentTarget.value);
              }}
            />
          </div>
        ) : null}
      </aside>

      <main
        ref={documentPanel}
        class="panel document-panel"
        id="document-panel"
        tabIndex={-1}
        aria-busy={document.status === "loading"}
        aria-label="Document"
      >
        <div class="document-content">
          {document.status === "idle" ? (
            <DocumentMessage title="No document selected">
              <p>Choose a Markdown file from the Files panel.</p>
            </DocumentMessage>
          ) : null}
          {document.status === "loading" ? (
            <div class="document-loading" role="status">
              <span>Loading document…</span>
              <span class="document-loading-line" aria-hidden="true" />
              <span class="document-loading-line is-short" aria-hidden="true" />
              <span class="document-loading-line" aria-hidden="true" />
            </div>
          ) : null}
          {document.status === "tooLarge" ? (
            <DocumentMessage title="This document is too large" state="error">
              <p>mdReview indexed the file but will not read or render it.</p>
            </DocumentMessage>
          ) : null}
          {document.status === "invalidUtf8" ? (
            <DocumentMessage title="This document is not valid UTF-8" state="error">
              <p>Save the file as UTF-8, then select it again.</p>
            </DocumentMessage>
          ) : null}
          {document.status === "removed" ? (
            <DocumentMessage title="This document is no longer available" state="error">
              <p>It may have been deleted or moved since the workspace was scanned.</p>
            </DocumentMessage>
          ) : null}
          {document.status === "error" ? (
            <DocumentMessage title="This document could not be opened" state="error">
              <p>Check that the file is readable, then select it again.</p>
            </DocumentMessage>
          ) : null}
          {document.status === "ready" ? (
            <ReviewedDocument
              model={document.model}
              currentDocumentPath={document.path}
              indexedDocumentPaths={indexedPaths}
              onNavigate={handleMarkdownNavigate}
              imageManager={imageManager}
              review={document.review}
              composer={composer}
              activeThreadId={activeThreadId}
              onActiveThread={setActiveThreadId}
              onStartTextComment={handleStartTextComment}
            />
          ) : null}
        </div>
      </main>

      <aside class="panel review-panel" aria-labelledby="review-heading">
        <ReviewPanel
          documentPath={"path" in document ? document.path : null}
          documentRevision={document.status === "ready" ? document.revision : null}
          review={document.status === "ready" ? document.review : null}
          composer={composer}
          activeThreadId={activeThreadId}
          documentChangeNotice={
            pendingDocumentChange && (composer !== null || operationEditorActive)
              ? DOCUMENT_CHANGED_NOTICE
              : null
          }
          onActiveThread={setActiveThreadId}
          onStartDocumentComment={handleStartDocumentComment}
          onDraft={(draft) => {
            transformComposer((current) =>
              current
                ? {
                    ...current,
                    draft,
                    error: null
                  }
                : current
            );
          }}
          onSubmit={handleSubmitComment}
          onCancel={handleCancelComposer}
          onOperation={handleReviewOperation}
          onEditorActiveChange={(active) => {
            operationEditorActiveRef.current = active;
            setOperationEditorActive(active);
            if (!active) {
              pollCoordinatorRef.current?.requestNow();
            }
          }}
        />
      </aside>

      <ThemeControl mode={themeMode} onChange={handleThemeChange} />
    </div>
  );
}
