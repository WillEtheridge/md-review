import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";

import type {
  CurrentRevisions,
  ReviewThread,
  TextReviewThread,
  TextThreadAnchor,
  ThreadStatus
} from "./api";
import { MarkdownDocument, type DocumentNavigation } from "./markdown/renderer";
import { mapDomRange, restoreDomRange } from "./markdown/selection";
import type { RenderModel } from "./markdown/types";
import { MessageMarkdown } from "./message-markdown";
import { orderTextThreads } from "./review-order";

export const MAX_ANCHOR_SOURCE_BYTES = 1024 * 1024;
export const MAX_MESSAGE_BODY_BYTES = 64 * 1024;

export type ReviewLoad =
  | {
      status: "ready";
      revision: string | null;
      threads: ReviewThread[];
    }
  | {
      status: "error";
      title: string;
      message: string;
    };

export interface ReviewComposer {
  kind: "document" | "text";
  anchor?: TextThreadAnchor;
  draft: string;
  submitting: boolean;
  error: string | null;
  conflict: CurrentRevisions | null;
}

interface HighlightRectangle {
  threadId: string;
  left: number;
  top: number;
  width: number;
  height: number;
}

type SelectionCandidate =
  | {
      state: "ready";
      anchor: TextThreadAnchor;
      left: number;
      top: number;
    }
  | {
      state: "invalid";
      message: string;
      left: number;
      top: number;
    };

function textThread(thread: ReviewThread): thread is TextReviewThread {
  return thread.anchor.type === "text";
}

function candidatePosition(range: Range, stage: HTMLElement): { left: number; top: number } {
  const rangeRect = range.getBoundingClientRect();
  const stageRect = stage.getBoundingClientRect();
  return {
    left: Math.max(8, rangeRect.left - stageRect.left + rangeRect.width / 2),
    top: Math.max(8, rangeRect.bottom - stageRect.top + 8)
  };
}

function selectionMessage(reason: string): string {
  if (reason === "invalid-utf8-boundary") {
    return "Adjust the selection so it does not split a Unicode character.";
  }
  return "Adjust the selection to begin and end on representable document text.";
}

function preferredScrollBehavior(): ScrollBehavior {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
}

export function ReviewedDocument({
  model,
  currentDocumentPath,
  indexedDocumentPaths,
  onNavigate,
  review,
  composer,
  activeThreadId,
  onActiveThread,
  onStartTextComment
}: {
  model: RenderModel;
  currentDocumentPath: string;
  indexedDocumentPaths: ReadonlySet<string>;
  onNavigate: (destination: DocumentNavigation) => void;
  review: ReviewLoad;
  composer: ReviewComposer | null;
  activeThreadId: string | null;
  onActiveThread: (threadId: string) => void;
  onStartTextComment: (anchor: TextThreadAnchor) => void;
}) {
  const stage = useRef<HTMLDivElement>(null);
  const documentRoot = useRef<HTMLElement>(null);
  const [rectangles, setRectangles] = useState<HighlightRectangle[]>([]);
  const rectanglesRef = useRef<HighlightRectangle[]>([]);
  const [candidate, setCandidate] = useState<SelectionCandidate | null>(null);

  const attachedThreads = useMemo(
    () =>
      review.status === "ready"
        ? review.threads.filter(
            (thread): thread is TextReviewThread =>
              textThread(thread) && thread.attachment.state === "attached"
          )
        : [],
    [review]
  );

  useLayoutEffect(() => {
    const root = documentRoot.current;
    const stageElement = stage.current;
    if (!root || !stageElement) {
      return;
    }

    let frame = 0;
    const update = (): void => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const stageRect = stageElement.getBoundingClientRect();
        const next: HighlightRectangle[] = [];
        for (const thread of attachedThreads) {
          if (thread.attachment.state !== "attached") {
            continue;
          }
          const range = restoreDomRange(thread.attachment.currentRange, root, model);
          if (!range) {
            continue;
          }
          for (const rect of range.getClientRects()) {
            if (rect.width === 0 || rect.height === 0) {
              continue;
            }
            next.push({
              threadId: thread.id,
              left: rect.left - stageRect.left,
              top: rect.top - stageRect.top,
              width: rect.width,
              height: rect.height
            });
          }
        }

        if (composer?.kind === "text" && composer.anchor) {
          const range = restoreDomRange(composer.anchor.range, root, model);
          if (range) {
            for (const rect of range.getClientRects()) {
              if (rect.width === 0 || rect.height === 0) {
                continue;
              }
              next.push({
                threadId: "__draft__",
                left: rect.left - stageRect.left,
                top: rect.top - stageRect.top,
                width: rect.width,
                height: rect.height
              });
            }
          }
        }
        rectanglesRef.current = next;
        setRectangles(next);
      });
    };

    update();
    const observer = new ResizeObserver(update);
    observer.observe(root);
    window.addEventListener("resize", update);
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      window.removeEventListener("resize", update);
    };
  }, [activeThreadId, attachedThreads, composer, model]);

  useEffect(() => {
    const root = documentRoot.current;
    if (!root || !activeThreadId) {
      return;
    }
    const thread = attachedThreads.find((candidateThread) => candidateThread.id === activeThreadId);
    if (!thread || thread.attachment.state !== "attached") {
      return;
    }
    const currentRange = thread.attachment.currentRange;
    const frame = requestAnimationFrame(() => {
      const range = restoreDomRange(currentRange, root, model);
      const element =
        range?.startContainer.nodeType === Node.TEXT_NODE
          ? range.startContainer.parentElement
          : (range?.startContainer as Element | undefined);
      element?.scrollIntoView({ block: "center", behavior: preferredScrollBehavior() });
    });
    return () => {
      cancelAnimationFrame(frame);
    };
  }, [activeThreadId, attachedThreads, model]);

  useEffect(() => {
    const root = documentRoot.current;
    const stageElement = stage.current;
    if (!root || !stageElement || composer !== null || review.status !== "ready") {
      setCandidate(null);
      return;
    }

    let frame = 0;
    const inspect = (): void => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const selection = window.getSelection();
        if (!selection || selection.rangeCount !== 1 || selection.isCollapsed) {
          setCandidate(null);
          return;
        }
        const range = selection.getRangeAt(0);
        if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) {
          setCandidate(null);
          return;
        }

        const position = candidatePosition(range, stageElement);
        const mapping = mapDomRange(range, root, model);
        if (mapping.decision === "reject") {
          if (mapping.reason === "empty-selection") {
            setCandidate(null);
            return;
          }
          setCandidate({
            state: "invalid",
            message: selectionMessage(mapping.reason),
            ...position
          });
          return;
        }
        if (mapping.anchor.end - mapping.anchor.start > MAX_ANCHOR_SOURCE_BYTES) {
          setCandidate({
            state: "invalid",
            message: "Select no more than 1 MiB of Markdown source.",
            ...position
          });
          return;
        }
        setCandidate({
          state: "ready",
          anchor: {
            type: "text",
            range: {
              start: mapping.anchor.start,
              end: mapping.anchor.end
            },
            source: mapping.anchor.source,
            text: mapping.anchor.text
          },
          ...position
        });
      });
    };

    document.addEventListener("selectionchange", inspect);
    root.addEventListener("pointerup", inspect);
    root.addEventListener("keyup", inspect);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("selectionchange", inspect);
      root.removeEventListener("pointerup", inspect);
      root.removeEventListener("keyup", inspect);
    };
  }, [composer, model, review.status]);

  const handleDocumentClick = (event: MouseEvent): void => {
    if (
      (event.target as Element | null)?.closest(".selection-action") ||
      !window.getSelection()?.isCollapsed
    ) {
      return;
    }
    const stageRectangle = stage.current?.getBoundingClientRect();
    if (!stageRectangle) {
      return;
    }
    const pointLeft = event.clientX - stageRectangle.left;
    const pointTop = event.clientY - stageRectangle.top;
    const hitIds = Array.from(
      new Set(
        rectanglesRef.current
          .filter(
            (rectangle) =>
              rectangle.threadId !== "__draft__" &&
              pointLeft >= rectangle.left &&
              pointLeft <= rectangle.left + rectangle.width &&
              pointTop >= rectangle.top &&
              pointTop <= rectangle.top + rectangle.height
          )
          .map((rectangle) => rectangle.threadId)
      )
    );
    if (hitIds.length === 0) {
      return;
    }
    const currentIndex = activeThreadId ? hitIds.indexOf(activeThreadId) : -1;
    const targetId = hitIds[(currentIndex + 1) % hitIds.length] ?? hitIds[0];
    if (targetId) {
      onActiveThread(targetId);
    }
  };

  return (
    <div ref={stage} class="review-document-stage" onClick={handleDocumentClick}>
      <MarkdownDocument
        documentRef={documentRoot}
        model={model}
        currentDocumentPath={currentDocumentPath}
        indexedDocumentPaths={indexedDocumentPaths}
        onNavigate={onNavigate}
      />
      <div class="review-highlight-layer" aria-hidden="true">
        {rectangles.map((rectangle, index) => (
          <span
            key={`${rectangle.threadId}:${String(index)}`}
            class={[
              "review-highlight",
              rectangle.threadId === activeThreadId ? "is-active" : "",
              rectangle.threadId === "__draft__" ? "is-draft" : ""
            ]
              .filter(Boolean)
              .join(" ")}
            data-thread-id={rectangle.threadId}
            style={{
              left: `${String(rectangle.left)}px`,
              top: `${String(rectangle.top)}px`,
              width: `${String(rectangle.width)}px`,
              height: `${String(rectangle.height)}px`
            }}
          />
        ))}
      </div>
      {candidate ? (
        <div
          class="selection-action"
          style={{
            left: `${String(candidate.left)}px`,
            top: `${String(candidate.top)}px`
          }}
        >
          {candidate.state === "ready" ? (
            <button
              type="button"
              aria-label="Comment on selected text"
              onMouseDown={(event) => {
                event.preventDefault();
              }}
              onClick={() => {
                onStartTextComment(candidate.anchor);
              }}
            >
              Comment
            </button>
          ) : (
            <p role="status">{candidate.message}</p>
          )}
        </div>
      ) : null}
    </div>
  );
}

export type ReviewOperation =
  | {
      kind: "reply";
      threadId: string;
      expectedDocumentRevision: string;
      expectedReviewRevision: string;
      body: string;
    }
  | {
      kind: "status";
      threadId: string;
      expectedDocumentRevision: string;
      expectedReviewRevision: string;
      status: "open" | "resolved";
    }
  | {
      kind: "delete";
      threadId: string;
      expectedDocumentRevision: string;
      expectedReviewRevision: string;
    };

interface OperationEditor {
  kind: "reply";
  threadId: string;
  expectedDocumentRevision: string;
  expectedReviewRevision: string;
  draft: string;
  submitting: boolean;
  error: string | null;
}

interface StatusFilters {
  open: boolean;
  handled: boolean;
  resolved: boolean;
}

function statusLabel(status: ThreadStatus): string {
  return status.charAt(0).toUpperCase() + status.slice(1);
}

function boundedThreadName(value: string): string {
  const normalised = value.replace(/\s+/gu, " ").trim() || "Untitled comment";
  const characters = Array.from(normalised);
  return characters.length <= 96 ? normalised : `${characters.slice(0, 95).join("")}…`;
}

function OperationComposer({
  editor,
  disabledReason,
  onDraft,
  onSubmit,
  onCancel
}: {
  editor: OperationEditor;
  disabledReason: string | null;
  onDraft: (draft: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const bodyBytes = new TextEncoder().encode(editor.draft).length;
  const empty = editor.draft.trim().length === 0;
  const tooLarge = bodyBytes > MAX_MESSAGE_BODY_BYTES;
  const label = "Reply";
  const inputID = `reply-message-${editor.threadId}`;
  const limitID = `${inputID}-limit`;

  return (
    <form
      class={`review-composer operation-composer operation-composer--${editor.kind}`}
      data-state={editor.error ? "error" : editor.submitting ? "submitting" : "ready"}
      onSubmit={(event) => {
        event.preventDefault();
        if (!empty && !tooLarge && !editor.submitting && disabledReason === null) {
          onSubmit();
        }
      }}
    >
      <label for={inputID}>{label}</label>
      <textarea
        id={inputID}
        autofocus
        rows={4}
        value={editor.draft}
        aria-describedby={tooLarge ? limitID : undefined}
        aria-invalid={tooLarge || undefined}
        onInput={(event) => {
          onDraft(event.currentTarget.value);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onCancel();
          } else if (
            event.key === "Enter" &&
            (event.ctrlKey || event.metaKey) &&
            !empty &&
            !tooLarge &&
            !editor.submitting &&
            disabledReason === null
          ) {
            event.preventDefault();
            onSubmit();
          }
        }}
      />
      <p id={tooLarge ? limitID : undefined} class={tooLarge ? "field-error" : "field-help"}>
        {tooLarge
          ? "Messages must be no more than 64 KiB of UTF-8."
          : `${String(bodyBytes)} of ${String(MAX_MESSAGE_BODY_BYTES)} bytes`}
      </p>
      {editor.error ? (
        <p class="composer-error" role="alert">
          {editor.error} Your draft has been kept.
        </p>
      ) : null}
      {disabledReason ? (
        <p class="composer-error" role="alert">
          {disabledReason} Your draft has been kept, but it cannot be submitted.
        </p>
      ) : null}
      <div class="composer-actions">
        <button
          type="submit"
          disabled={empty || tooLarge || editor.submitting || disabledReason !== null}
        >
          {editor.submitting ? "Saving…" : label}
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
      <p class="keyboard-hint">Cmd/Ctrl+Enter to save · Esc to cancel</p>
    </form>
  );
}

function OverflowMenu({
  label,
  children,
  onTriggerRef
}: {
  label: string;
  children: (close: () => void) => preact.ComponentChildren;
  onTriggerRef?: (element: HTMLButtonElement | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  const close = (): void => {
    setOpen(false);
  };

  useEffect(() => {
    if (!open) {
      return;
    }
    const closeOnPointerDown = (event: PointerEvent): void => {
      if (!root.current?.contains(event.target as Node)) {
        close();
      }
    };
    const closeOnEscape = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
        triggerRef.current?.focus();
      }
    };
    document.addEventListener("pointerdown", closeOnPointerDown);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnPointerDown);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);

  return (
    <div ref={root} class="overflow-menu">
      <button
        ref={(element) => {
          triggerRef.current = element;
          onTriggerRef?.(element);
        }}
        type="button"
        class="overflow-menu-trigger"
        aria-label={label}
        aria-expanded={open}
        onClick={() => {
          setOpen((current) => !current);
        }}
      >
        <span aria-hidden="true">•••</span>
      </button>
      {open ? <div class="overflow-menu-list">{children(close)}</div> : null}
    </div>
  );
}

function ThreadCard({
  thread,
  active,
  interactive,
  editor,
  editorDisabledReason,
  pending,
  actionError,
  onTargetRef,
  onSelect,
  onStartReply,
  onStatus,
  deleteConfirmation,
  onRequestDelete,
  onConfirmDelete,
  onCancelDelete,
  onEditorDraft,
  onEditorSubmit,
  onEditorCancel
}: {
  thread: ReviewThread;
  active: boolean;
  interactive: boolean;
  editor: OperationEditor | null;
  editorDisabledReason: string | null;
  pending: boolean;
  actionError: string | null;
  onTargetRef: (element: HTMLButtonElement | null) => void;
  onSelect: () => void;
  onStartReply: (trigger: HTMLButtonElement) => void;
  onStatus: (status: "open" | "resolved", trigger: HTMLButtonElement) => void;
  deleteConfirmation: boolean;
  onRequestDelete: (trigger: HTMLButtonElement | null) => void;
  onConfirmDelete: (trigger: HTMLButtonElement) => void;
  onCancelDelete: () => void;
  onEditorDraft: (draft: string) => void;
  onEditorSubmit: () => void;
  onEditorCancel: () => void;
}) {
  const rawLabel =
    thread.anchor.type === "document"
      ? "Document comment"
      : thread.anchor.text || thread.anchor.source;
  const threadName = boundedThreadName(rawLabel);
  const attachmentState = thread.anchor.type === "document" ? "document" : thread.attachment.state;
  const attachmentLabel =
    thread.anchor.type === "text" && thread.attachment.state === "detached"
      ? "Detached"
      : thread.anchor.type === "text"
        ? "Text"
        : "Document";
  const canOperate = interactive && !pending && editor === null;
  const canDelete = canOperate && thread.messages.length === 1;
  const menuTriggerRef = useRef<HTMLButtonElement | null>(null);
  const hasThreadMenu = canOperate && (thread.status === "resolved" || canDelete);

  return (
    <article
      class={`thread-card${active ? " is-active" : ""}`}
      data-thread-id={thread.id}
      data-status={thread.status}
      data-attachment={attachmentState}
      data-active={String(active)}
      aria-busy={pending}
    >
      <div class="thread-card-header">
        <button
          ref={onTargetRef}
          type="button"
          class="thread-target"
          aria-current={active ? "true" : undefined}
          aria-label={`${threadName}. ${attachmentLabel}. ${statusLabel(thread.status)}.`}
          onClick={onSelect}
        >
          <span class="thread-anchor">{threadName}</span>
          <span class="thread-metadata">
            <span class="thread-attachment">{attachmentLabel}</span>
            <span aria-hidden="true"> · </span>
            <span class="thread-status" data-status={thread.status}>
              {statusLabel(thread.status)}
            </span>
          </span>
        </button>
        {hasThreadMenu ? (
          <OverflowMenu
            label={`More actions for ${threadName}`}
            onTriggerRef={(element) => {
              menuTriggerRef.current = element;
            }}
          >
            {(close) => (
              <>
                {thread.status === "resolved" ? (
                  <button
                    type="button"
                    onClick={(event) => {
                      close();
                      onStatus("open", event.currentTarget);
                    }}
                  >
                    Reopen
                  </button>
                ) : null}
                {canDelete ? (
                  <button
                    type="button"
                    onClick={() => {
                      close();
                      onRequestDelete(menuTriggerRef.current);
                    }}
                  >
                    Delete thread
                  </button>
                ) : null}
              </>
            )}
          </OverflowMenu>
        ) : null}
      </div>
      <ol class="thread-messages">
        {thread.messages.map((message) => (
          <li key={message.id} data-message-id={message.id}>
            <div class="message-heading">
              <p class="message-author">
                {message.author.name}
                <span class="message-author-type"> · {message.author.type}</span>
                {message.editedAt ? " · edited" : ""}
              </p>
            </div>
            <MessageMarkdown source={message.body} />
          </li>
        ))}
      </ol>
      {editor?.kind === "reply" && editor.threadId === thread.id ? (
        <OperationComposer
          editor={editor}
          disabledReason={editorDisabledReason}
          onDraft={onEditorDraft}
          onSubmit={onEditorSubmit}
          onCancel={onEditorCancel}
        />
      ) : null}
      {canOperate ? (
        <div class="thread-actions">
          <button
            type="button"
            aria-label={`Reply to ${threadName}`}
            onClick={(event) => {
              onStartReply(event.currentTarget);
            }}
          >
            Reply
          </button>
          {thread.status !== "resolved" ? (
            <button
              type="button"
              class="thread-action-primary"
              aria-label={`Resolve ${threadName}`}
              onClick={(event) => {
                onStatus("resolved", event.currentTarget);
              }}
            >
              Resolve
            </button>
          ) : null}
        </div>
      ) : null}
      {deleteConfirmation ? (
        <section class="delete-confirmation" role="alert">
          <p>Delete this thread? This cannot be undone.</p>
          <div>
            <button
              type="button"
              autofocus
              onClick={(event) => {
                onConfirmDelete(event.currentTarget);
              }}
            >
              Delete thread
            </button>
            <button type="button" onClick={onCancelDelete}>
              Cancel
            </button>
          </div>
        </section>
      ) : null}
      {pending ? (
        <p class="thread-action-status" role="status">
          Saving review change…
        </p>
      ) : null}
      {actionError ? (
        <p class="composer-error thread-action-error" role="alert">
          {actionError}
        </p>
      ) : null}
    </article>
  );
}

function Composer({
  composer,
  disabledReason,
  onDraft,
  onSubmit,
  onCancel
}: {
  composer: ReviewComposer;
  disabledReason: string | null;
  onDraft: (draft: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const bodyBytes = new TextEncoder().encode(composer.draft).length;
  const empty = composer.draft.trim().length === 0;
  const tooLarge = bodyBytes > MAX_MESSAGE_BODY_BYTES;

  return (
    <form
      class="review-composer"
      data-state={
        composer.conflict
          ? "conflict"
          : composer.error
            ? "error"
            : composer.submitting
              ? "submitting"
              : "ready"
      }
      onSubmit={(event) => {
        event.preventDefault();
        if (!empty && !tooLarge && !composer.submitting && disabledReason === null) {
          onSubmit();
        }
      }}
    >
      {composer.kind === "document" ? <h3>Comment on document</h3> : null}
      {composer.kind === "text" && composer.anchor ? (
        <blockquote>{composer.anchor.text || composer.anchor.source}</blockquote>
      ) : null}
      <label for="review-message">Comment</label>
      <textarea
        id="review-message"
        autofocus
        rows={6}
        value={composer.draft}
        aria-describedby={tooLarge ? "review-message-limit" : undefined}
        aria-invalid={tooLarge || undefined}
        onInput={(event) => {
          onDraft(event.currentTarget.value);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onCancel();
          } else if (
            event.key === "Enter" &&
            (event.ctrlKey || event.metaKey) &&
            !empty &&
            !tooLarge &&
            !composer.submitting &&
            disabledReason === null
          ) {
            event.preventDefault();
            onSubmit();
          }
        }}
      />
      {tooLarge ? (
        <p id="review-message-limit" class="field-error">
          Comments must be no more than 64 KiB of UTF-8.
        </p>
      ) : null}
      {composer.error ? (
        <p class="composer-error" role="alert">
          {composer.error}
          {composer.conflict
            ? " Your draft and frozen selection have been kept."
            : " Your draft has been kept."}
        </p>
      ) : null}
      {disabledReason ? (
        <p class="composer-error" role="alert">
          {disabledReason} Your draft has been kept, but it cannot be submitted.
        </p>
      ) : null}
      <div class="composer-actions">
        <button
          type="submit"
          disabled={empty || tooLarge || composer.submitting || disabledReason !== null}
        >
          {composer.submitting ? "Saving…" : "Save comment"}
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
      <p class="keyboard-hint">Cmd/Ctrl+Enter to save · Esc to cancel</p>
    </form>
  );
}

export function ReviewPanel({
  documentPath,
  documentRevision,
  review,
  composer,
  activeThreadId,
  documentChangeNotice,
  onActiveThread,
  onStartDocumentComment,
  onDraft,
  onSubmit,
  onCancel,
  onOperation,
  onEditorActiveChange
}: {
  documentPath: string | null;
  documentRevision: string | null;
  review: ReviewLoad | null;
  composer: ReviewComposer | null;
  activeThreadId: string | null;
  documentChangeNotice: string | null;
  onActiveThread: (threadId: string) => void;
  onStartDocumentComment: () => void;
  onDraft: (draft: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
  onOperation: (operation: ReviewOperation) => Promise<void>;
  onEditorActiveChange: (active: boolean) => void;
}) {
  const [filters, setFilters] = useState<StatusFilters>({
    open: true,
    handled: true,
    resolved: false
  });
  const [editor, setEditor] = useState<OperationEditor | null>(null);
  const [pendingThreadId, setPendingThreadId] = useState<string | null>(null);
  const [deleteConfirmationThreadId, setDeleteConfirmationThreadId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<{ threadId: string; message: string } | null>(
    null
  );
  const originRef = useRef<HTMLButtonElement | null>(null);
  const deleteTriggerRef = useRef<HTMLButtonElement | null>(null);
  const addDocumentButtonRef = useRef<HTMLButtonElement>(null);
  const targetRefs = useRef(new Map<string, HTMLButtonElement>());
  const editorDocumentPathRef = useRef(documentPath);

  const readyReview = review?.status === "ready" ? review : null;
  const filteredThreads = readyReview?.threads.filter((thread) => filters[thread.status]) ?? [];
  const documentThreads = filteredThreads.filter((thread) => thread.anchor.type === "document");
  const textThreads = orderTextThreads(filteredThreads.filter(textThread));
  const visibleThreads = [...documentThreads, ...textThreads];
  const editorThread = editor
    ? readyReview?.threads.find((thread) => thread.id === editor.threadId)
    : undefined;
  const editorIsMounted = editor
    ? visibleThreads.some((thread) => thread.id === editor.threadId)
    : false;
  const editorDisabledReason = editor
    ? review?.status === "error"
      ? `${review.title}. ${review.message}`
      : !editorThread
        ? "This review item is no longer available."
        : null
    : null;
  const composerDisabledReason =
    composer && review?.status === "error" ? `${review.title}. ${review.message}` : null;

  useEffect(() => {
    if (editorDocumentPathRef.current === documentPath) {
      return;
    }
    editorDocumentPathRef.current = documentPath;
    const hadActiveEditor = editor !== null;
    setEditor(null);
    setPendingThreadId(null);
    setDeleteConfirmationThreadId(null);
    setActionError(null);
    if (hadActiveEditor) {
      onEditorActiveChange(false);
    }
  }, [documentPath, editor, onEditorActiveChange]);

  const restoreOriginFocus = (threadId: string): void => {
    requestAnimationFrame(() => {
      const threadTarget = targetRefs.current.get(threadId);
      if (threadTarget?.isConnected) {
        threadTarget.focus();
      } else if (originRef.current?.isConnected) {
        originRef.current.focus();
      } else {
        addDocumentButtonRef.current?.focus();
      }
    });
  };

  const handleStartReply = (threadId: string, trigger: HTMLButtonElement): void => {
    if (!documentRevision || !readyReview?.revision) {
      return;
    }
    originRef.current = trigger;
    setActionError(null);
    setEditor({
      kind: "reply",
      threadId,
      expectedDocumentRevision: documentRevision,
      expectedReviewRevision: readyReview.revision,
      draft: "",
      submitting: false,
      error: null
    });
    onEditorActiveChange(true);
  };

  const handleEditorSubmit = (): void => {
    if (!editor || editor.submitting) {
      return;
    }
    const submittedThreadId = editor.threadId;
    const operation: ReviewOperation = {
      kind: "reply",
      threadId: editor.threadId,
      expectedDocumentRevision: editor.expectedDocumentRevision,
      expectedReviewRevision: editor.expectedReviewRevision,
      body: editor.draft
    };
    setEditor({ ...editor, submitting: true, error: null });
    void onOperation(operation)
      .then(() => {
        setEditor(null);
        onEditorActiveChange(false);
        restoreOriginFocus(submittedThreadId);
      })
      .catch((error: unknown) => {
        setEditor((current) =>
          current
            ? {
                ...current,
                submitting: false,
                error:
                  error instanceof Error ? error.message : "The review change could not be saved."
              }
            : current
        );
      });
  };

  const handleImmediateOperation = (
    operation: ReviewOperation,
    trigger: HTMLButtonElement
  ): void => {
    originRef.current = trigger;
    setActionError(null);
    setPendingThreadId(operation.threadId);
    void onOperation(operation)
      .then(() => {
        setPendingThreadId(null);
        if (operation.kind === "delete") {
          requestAnimationFrame(() => {
            addDocumentButtonRef.current?.focus();
          });
        } else {
          restoreOriginFocus(operation.threadId);
        }
      })
      .catch((error: unknown) => {
        setPendingThreadId(null);
        setActionError({
          threadId: operation.threadId,
          message: error instanceof Error ? error.message : "The review change could not be saved."
        });
        restoreOriginFocus(operation.threadId);
      });
  };

  const handleRequestDelete = (threadId: string, trigger: HTMLButtonElement | null): void => {
    deleteTriggerRef.current = trigger;
    setDeleteConfirmationThreadId(threadId);
  };

  const handleCancelDelete = (threadId: string): void => {
    setDeleteConfirmationThreadId(null);
    requestAnimationFrame(() => {
      if (deleteTriggerRef.current?.isConnected) {
        deleteTriggerRef.current.focus();
      } else {
        restoreOriginFocus(threadId);
      }
    });
  };

  return (
    <>
      <header class="panel-header review-header">
        <h2 id="review-heading">Comments</h2>
        {review?.status === "ready" && !composer ? (
          <button ref={addDocumentButtonRef} type="button" onClick={onStartDocumentComment}>
            Comment on document
          </button>
        ) : null}
      </header>
      {documentChangeNotice ? (
        <p class="composer-error" role="status">
          {documentChangeNotice}
        </p>
      ) : null}
      {composer ? (
        <Composer
          composer={composer}
          disabledReason={composerDisabledReason}
          onDraft={onDraft}
          onSubmit={onSubmit}
          onCancel={onCancel}
        />
      ) : null}
      {review === null ? (
        <p class="panel-status">Choose a readable document to review it.</p>
      ) : null}
      {review?.status === "error" ? (
        <section class="review-empty" data-state="error" role="alert">
          <h3>{review.title}</h3>
          <p>{review.message}</p>
        </section>
      ) : null}
      {readyReview && readyReview.threads.length > 0 ? (
        <section class="review-controls" aria-label="Review filters">
          <fieldset>
            <legend>Show statuses</legend>
            {(["open", "handled", "resolved"] as const).map((status) => (
              <label key={status}>
                <input
                  type="checkbox"
                  checked={filters[status]}
                  onChange={(event) => {
                    setFilters((current) => ({
                      ...current,
                      [status]: event.currentTarget.checked
                    }));
                  }}
                />
                {statusLabel(status)}
              </label>
            ))}
          </fieldset>
        </section>
      ) : null}
      {readyReview && readyReview.threads.length > 0 && visibleThreads.length === 0 ? (
        <p class="panel-status">No comments match the selected status filters.</p>
      ) : null}
      {documentThreads.length > 0 ? (
        <section class="thread-section" aria-labelledby="document-threads-heading">
          <h3 id="document-threads-heading">Document</h3>
          {documentThreads.map((thread) => (
            <ThreadCard
              key={thread.id}
              thread={thread}
              active={thread.id === activeThreadId}
              interactive={!composer && readyReview?.revision !== null}
              editor={editor?.threadId === thread.id ? editor : null}
              editorDisabledReason={editorDisabledReason}
              pending={pendingThreadId === thread.id}
              actionError={actionError?.threadId === thread.id ? actionError.message : null}
              onTargetRef={(element) => {
                if (element) {
                  targetRefs.current.set(thread.id, element);
                } else {
                  targetRefs.current.delete(thread.id);
                }
              }}
              onSelect={() => {
                onActiveThread(thread.id);
              }}
              onStartReply={(trigger) => {
                handleStartReply(thread.id, trigger);
              }}
              onStatus={(status, trigger) => {
                if (documentRevision && readyReview?.revision) {
                  handleImmediateOperation(
                    {
                      kind: "status",
                      threadId: thread.id,
                      expectedDocumentRevision: documentRevision,
                      expectedReviewRevision: readyReview.revision,
                      status
                    },
                    trigger
                  );
                }
              }}
              deleteConfirmation={deleteConfirmationThreadId === thread.id}
              onRequestDelete={(trigger) => {
                handleRequestDelete(thread.id, trigger);
              }}
              onConfirmDelete={(trigger) => {
                setDeleteConfirmationThreadId(null);
                if (documentRevision && readyReview?.revision) {
                  handleImmediateOperation(
                    {
                      kind: "delete",
                      threadId: thread.id,
                      expectedDocumentRevision: documentRevision,
                      expectedReviewRevision: readyReview.revision
                    },
                    trigger
                  );
                }
              }}
              onCancelDelete={() => {
                handleCancelDelete(thread.id);
              }}
              onEditorDraft={(draft) => {
                setEditor((current) => (current ? { ...current, draft, error: null } : current));
              }}
              onEditorSubmit={handleEditorSubmit}
              onEditorCancel={() => {
                const threadId = editor?.threadId ?? thread.id;
                setEditor(null);
                onEditorActiveChange(false);
                restoreOriginFocus(threadId);
              }}
            />
          ))}
        </section>
      ) : null}
      {textThreads.length > 0 ? (
        <section class="thread-section" aria-labelledby="text-threads-heading">
          <h3 id="text-threads-heading">Text</h3>
          {textThreads.map((thread) => (
            <ThreadCard
              key={thread.id}
              thread={thread}
              active={thread.id === activeThreadId}
              interactive={!composer && readyReview?.revision !== null}
              editor={editor?.threadId === thread.id ? editor : null}
              editorDisabledReason={editorDisabledReason}
              pending={pendingThreadId === thread.id}
              actionError={actionError?.threadId === thread.id ? actionError.message : null}
              onTargetRef={(element) => {
                if (element) {
                  targetRefs.current.set(thread.id, element);
                } else {
                  targetRefs.current.delete(thread.id);
                }
              }}
              onSelect={() => {
                onActiveThread(thread.id);
              }}
              onStartReply={(trigger) => {
                handleStartReply(thread.id, trigger);
              }}
              onStatus={(status, trigger) => {
                if (documentRevision && readyReview?.revision) {
                  handleImmediateOperation(
                    {
                      kind: "status",
                      threadId: thread.id,
                      expectedDocumentRevision: documentRevision,
                      expectedReviewRevision: readyReview.revision,
                      status
                    },
                    trigger
                  );
                }
              }}
              deleteConfirmation={deleteConfirmationThreadId === thread.id}
              onRequestDelete={(trigger) => {
                handleRequestDelete(thread.id, trigger);
              }}
              onConfirmDelete={(trigger) => {
                setDeleteConfirmationThreadId(null);
                if (documentRevision && readyReview?.revision) {
                  handleImmediateOperation(
                    {
                      kind: "delete",
                      threadId: thread.id,
                      expectedDocumentRevision: documentRevision,
                      expectedReviewRevision: readyReview.revision
                    },
                    trigger
                  );
                }
              }}
              onCancelDelete={() => {
                handleCancelDelete(thread.id);
              }}
              onEditorDraft={(draft) => {
                setEditor((current) => (current ? { ...current, draft, error: null } : current));
              }}
              onEditorSubmit={handleEditorSubmit}
              onEditorCancel={() => {
                const threadId = editor?.threadId ?? thread.id;
                setEditor(null);
                onEditorActiveChange(false);
                restoreOriginFocus(threadId);
              }}
            />
          ))}
        </section>
      ) : null}
      {editor && !editorIsMounted ? (
        <section class="thread-section" aria-labelledby="preserved-editor-heading">
          <h3 id="preserved-editor-heading">Reply draft</h3>
          <OperationComposer
            editor={editor}
            disabledReason={editorDisabledReason}
            onDraft={(draft) => {
              setEditor((current) => (current ? { ...current, draft, error: null } : current));
            }}
            onSubmit={handleEditorSubmit}
            onCancel={() => {
              const threadId = editor.threadId;
              setEditor(null);
              onEditorActiveChange(false);
              restoreOriginFocus(threadId);
            }}
          />
        </section>
      ) : null}
    </>
  );
}
