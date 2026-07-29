export interface LeafMapping {
  id: string;
  text: string;
  boundaries: Array<number | null>;
}

export interface TextAnchor {
  start: number;
  end: number;
  source: string;
  text: string;
}

export interface AcceptedMapping {
  decision: "accept";
  anchor: TextAnchor;
  sourceStartUtf16: number;
  sourceEndUtf16: number;
  startLeafId: string;
  startLeafOffset: number;
  endLeafId: string;
  endLeafOffset: number;
}

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

export interface RenderModel {
  source: string;
  tree: import("hast").Root;
  leaves: Map<string, LeafMapping>;
  annotations: WeakMap<import("hast").Text, LeafMapping>;
  images: WeakMap<import("hast").Element, ImageDescriptor>;
}

export interface ImageDescriptor {
  reference: string;
  alt: string;
  title?: string;
}
