/**
 * The browser-side mapping model bridges three coordinate systems: authored
 * Markdown UTF-16 offsets, rendered DOM text nodes, and persisted UTF-8 byte
 * anchors. It is rebuilt whenever the source document changes and is never
 * written to the review sidecar.
 */

/** One rendered text leaf and the source boundaries it can represent. */
export interface LeafMapping {
  /** Stable only for the lifetime of this render model; used in DOM data attributes. */
  id: string;
  /** Exact textContent expected in the corresponding rendered leaf. */
  text: string;
  /**
   * One entry for every rendered UTF-16 boundary (`text.length + 1`). A null
   * entry means that the boundary is ambiguous or synthetic and cannot safely
   * become a persisted source range.
   */
  boundaries: Array<number | null>;
}

/** A persisted text anchor expressed in UTF-8 byte offsets plus display text. */
export interface TextAnchor {
  /** Inclusive UTF-8 byte offset in the original Markdown. */
  start: number;
  /** Exclusive UTF-8 byte offset in the original Markdown. */
  end: number;
  /** Exact authored source bytes decoded as a JavaScript string. */
  source: string;
  /** Text the browser selected, which can differ from source Markdown syntax. */
  text: string;
}

/** Successful DOM selection mapping, including temporary coordinates for UI restoration. */
export interface AcceptedMapping {
  decision: "accept";
  anchor: TextAnchor;
  /** Source offsets in JavaScript UTF-16 units, before conversion to anchor bytes. */
  sourceStartUtf16: number;
  sourceEndUtf16: number;
  /** Render-leaf coordinates used to restore the selection while this model is mounted. */
  startLeafId: string;
  startLeafOffset: number;
  endLeafId: string;
  endLeafOffset: number;
}

/** A deliberately rejected selection and the invariant that made it unsafe to persist. */
export interface RejectedMapping {
  decision: "reject";
  reason:
    | "empty-selection"
    | "unmapped-start"
    | "unmapped-end"
    | "ambiguous-start"
    | "ambiguous-end"
    | "reversed-range"
    | "invalid-utf8-boundary";
}

export type MappingResult = AcceptedMapping | RejectedMapping;

/**
 * Immutable render products for one exact source string. `annotations` is
 * keyed by parser text nodes, while `leaves` is keyed by DOM-visible IDs;
 * `images` is separate so validated asset descriptors do not reintroduce raw
 * Markdown URLs into the sanitized tree.
 */
export interface RenderModel {
  /** Exact source used to create `tree` and every mapping boundary. */
  source: string;
  /** Sanitized HAST tree used as the input to the Preact renderer. */
  tree: import("hast").Root;
  /** Strongly held leaf lookup used by selection mapping and restoration. */
  leaves: Map<string, LeafMapping>;
  /** Parser-node annotations held weakly so they do not extend node lifetimes. */
  annotations: WeakMap<import("hast").Text, LeafMapping>;
  /** Validated local-image descriptors keyed by their sanitized HAST elements. */
  images: WeakMap<import("hast").Element, ImageDescriptor>;
}

/** A local image reference that has passed the renderer's URL policy. */
export interface ImageDescriptor {
  /** Workspace-relative reference resolved by the server's asset endpoint. */
  reference: string;
  /** Alt text preserved for the eventual real image and placeholder label. */
  alt: string;
  /** Optional authored title, omitted when empty. */
  title?: string;
}
