import { utf16OffsetToUtf8, utf8OffsetToUtf16 } from "./alignment";
import type { AcceptedMapping, LeafMapping, MappingResult, RenderModel, TextAnchor } from "./types";

interface MappedDomPoint {
  node: Text;
  leaf: LeafMapping;
  offset: number;
}

function leafForTextNode(node: Text, model: RenderModel): MappedDomPoint | undefined {
  const element = node.parentElement?.closest<HTMLElement>("[data-md-leaf]");
  if (!element) {
    return undefined;
  }
  const id = element.dataset.mdLeaf;
  if (!id) {
    return undefined;
  }
  const leaf = model.leaves.get(id);
  if (!leaf || element.textContent !== leaf.text) {
    return undefined;
  }
  return { node, leaf, offset: 0 };
}

function firstMappedText(node: Node, model: RenderModel): MappedDomPoint | undefined {
  if (node.nodeType === Node.TEXT_NODE) {
    return leafForTextNode(node as Text, model);
  }
  for (const child of node.childNodes) {
    const mapped = firstMappedText(child, model);
    if (mapped) {
      return mapped;
    }
  }
  return undefined;
}

function lastMappedText(node: Node, model: RenderModel): MappedDomPoint | undefined {
  if (node.nodeType === Node.TEXT_NODE) {
    const mapped = leafForTextNode(node as Text, model);
    if (mapped) {
      mapped.offset = node.textContent?.length ?? 0;
    }
    return mapped;
  }
  for (let index = node.childNodes.length - 1; index >= 0; index -= 1) {
    const child = node.childNodes[index];
    if (!child) {
      continue;
    }
    const mapped = lastMappedText(child, model);
    if (mapped) {
      return mapped;
    }
  }
  return undefined;
}

function nextMappedText(
  container: Node,
  offset: number,
  root: Node,
  model: RenderModel
): MappedDomPoint | undefined {
  if (container.nodeType === Node.TEXT_NODE) {
    const mapped = leafForTextNode(container as Text, model);
    if (mapped) {
      mapped.offset = offset;
    }
    return mapped;
  }

  for (let index = offset; index < container.childNodes.length; index += 1) {
    const child = container.childNodes[index];
    if (!child) {
      continue;
    }
    const mapped = firstMappedText(child, model);
    if (mapped) {
      return mapped;
    }
  }

  let current: Node | null = container;
  while (current && current !== root) {
    let sibling = current.nextSibling;
    while (sibling) {
      const mapped = firstMappedText(sibling, model);
      if (mapped) {
        return mapped;
      }
      sibling = sibling.nextSibling;
    }
    current = current.parentNode;
  }
  return undefined;
}

function previousMappedText(
  container: Node,
  offset: number,
  root: Node,
  model: RenderModel
): MappedDomPoint | undefined {
  if (container.nodeType === Node.TEXT_NODE) {
    const mapped = leafForTextNode(container as Text, model);
    if (mapped) {
      mapped.offset = offset;
    }
    return mapped;
  }

  for (let index = offset - 1; index >= 0; index -= 1) {
    const child = container.childNodes[index];
    if (!child) {
      continue;
    }
    const mapped = lastMappedText(child, model);
    if (mapped) {
      return mapped;
    }
  }

  let current: Node | null = container;
  while (current && current !== root) {
    let sibling = current.previousSibling;
    while (sibling) {
      const mapped = lastMappedText(sibling, model);
      if (mapped) {
        return mapped;
      }
      sibling = sibling.previousSibling;
    }
    current = current.parentNode;
  }
  return undefined;
}

function mappedBoundary(point: MappedDomPoint): {
  sourceOffset: number | null;
  leafOffset: number;
} {
  return {
    sourceOffset: point.leaf.boundaries[point.offset] ?? null,
    leafOffset: point.offset
  };
}

export function mapDomRange(range: Range, root: HTMLElement, model: RenderModel): MappingResult {
  if (range.collapsed || range.toString().length === 0) {
    return { decision: "reject", reason: "empty-selection" };
  }

  const startPoint = nextMappedText(range.startContainer, range.startOffset, root, model);
  if (!startPoint) {
    return { decision: "reject", reason: "unmapped-start" };
  }

  const endPoint = previousMappedText(range.endContainer, range.endOffset, root, model);
  if (!endPoint) {
    return { decision: "reject", reason: "unmapped-end" };
  }

  const start = mappedBoundary(startPoint);
  if (start.sourceOffset === null) {
    return { decision: "reject", reason: "ambiguous-start" };
  }

  const end = mappedBoundary(endPoint);
  if (end.sourceOffset === null) {
    return { decision: "reject", reason: "ambiguous-end" };
  }

  if (start.sourceOffset >= end.sourceOffset) {
    return { decision: "reject", reason: "reversed-range" };
  }

  try {
    // Browser ranges use UTF-16 code units while persisted anchors use exact
    // half-open UTF-8 byte ranges. Surrogate interiors are rejected rather
    // than rounded, so every accepted boundary names the original bytes.
    const startByte = utf16OffsetToUtf8(model.source, start.sourceOffset);
    const endByte = utf16OffsetToUtf8(model.source, end.sourceOffset);
    const result: AcceptedMapping = {
      decision: "accept",
      anchor: {
        start: startByte,
        end: endByte,
        source: model.source.slice(start.sourceOffset, end.sourceOffset),
        text: range.toString()
      },
      sourceStartUtf16: start.sourceOffset,
      sourceEndUtf16: end.sourceOffset,
      startLeafId: startPoint.leaf.id,
      startLeafOffset: start.leafOffset,
      endLeafId: endPoint.leaf.id,
      endLeafOffset: end.leafOffset
    };
    return result;
  } catch {
    return { decision: "reject", reason: "invalid-utf8-boundary" };
  }
}

function pointForSourceOffset(
  sourceOffset: number,
  model: RenderModel,
  root: HTMLElement,
  preferLast: boolean
): { node: Text; offset: number } | undefined {
  const matches: Array<{ node: Text; offset: number }> = [];
  for (const [id, leaf] of model.leaves) {
    for (let offset = 0; offset < leaf.boundaries.length; offset += 1) {
      if (leaf.boundaries[offset] !== sourceOffset) {
        continue;
      }
      const element = root.querySelector<HTMLElement>(`[data-md-leaf="${CSS.escape(id)}"]`);
      const node = element?.firstChild;
      if (node?.nodeType === Node.TEXT_NODE) {
        matches.push({ node: node as Text, offset });
      }
    }
  }
  return preferLast ? matches.at(-1) : matches[0];
}

export function restoreDomRange(
  anchor: Pick<TextAnchor, "start" | "end">,
  root: HTMLElement,
  model: RenderModel
): Range | undefined {
  let sourceStart: number;
  let sourceEnd: number;
  try {
    sourceStart = utf8OffsetToUtf16(model.source, anchor.start);
    sourceEnd = utf8OffsetToUtf16(model.source, anchor.end);
  } catch {
    return undefined;
  }

  const start = pointForSourceOffset(sourceStart, model, root, false);
  const end = pointForSourceOffset(sourceEnd, model, root, true);
  if (!start || !end) {
    return undefined;
  }

  const range = document.createRange();
  range.setStart(start.node, start.offset);
  range.setEnd(end.node, end.offset);
  return range;
}
