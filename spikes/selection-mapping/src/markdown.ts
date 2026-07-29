import type { Element, Nodes, Root, Text } from "hast";
import { h, type ComponentChild } from "preact";
import rehypeHighlight from "rehype-highlight";
import rehypeRaw from "rehype-raw";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";

import {
  alignRenderedText,
  fencedCodeContentSpan,
  inlineCodeContentSpan
} from "./alignment";
import type { LeafMapping, RenderModel } from "./types";

function textContent(node: Nodes): string {
  if (node.type === "text") {
    return node.value;
  }
  if ("children" in node) {
    return node.children.map(textContent).join("");
  }
  return "";
}

function positionedSpan(
  node: { position?: Nodes["position"] }
): { start: number; end: number } | undefined {
  const start = node.position?.start.offset;
  const end = node.position?.end.offset;
  if (start === undefined || end === undefined) {
    return undefined;
  }
  return { start, end };
}

function collectTextNodes(node: Nodes): Text[] {
  if (node.type === "text") {
    return [node];
  }
  if ("children" in node) {
    return node.children.flatMap(collectTextNodes);
  }
  return [];
}

function annotateCodeElement(
  source: string,
  code: Element,
  fenced: boolean,
  nextLeafId: () => string,
  leaves: Map<string, LeafMapping>,
  annotations: WeakMap<Text, LeafMapping>
): void {
  const span = positionedSpan(code);
  if (!span) {
    return;
  }

  const rendered = textContent(code);
  const contentSpan = fenced
    ? fencedCodeContentSpan(source, span.start, span.end, rendered)
    : inlineCodeContentSpan(source, span.start, span.end);
  if (!contentSpan) {
    return;
  }

  const alignment = alignRenderedText(
    contentSpan.raw,
    rendered,
    contentSpan.start,
    {
      decodeMarkdown: false,
      normaliseLineEndingsToSpace: !fenced
    }
  );
  if (alignment.error) {
    return;
  }

  let cursor = 0;
  for (const text of collectTextNodes(code)) {
    const start = cursor;
    const end = cursor + text.value.length;
    const leaf: LeafMapping = {
      id: nextLeafId(),
      text: text.value,
      boundaries: alignment.boundaries.slice(start, end + 1)
    };
    leaves.set(leaf.id, leaf);
    annotations.set(text, leaf);
    cursor = end;
  }
}

function annotateSyntheticText(
  source: string,
  text: Text,
  ancestor: Element | undefined,
  nextLeafId: () => string,
  leaves: Map<string, LeafMapping>,
  annotations: WeakMap<Text, LeafMapping>
): void {
  const ancestorSpan = ancestor ? positionedSpan(ancestor) : undefined;
  const leading = text.value.match(/^\s*/u)?.[0].length ?? 0;
  const trailing = text.value.match(/\s*$/u)?.[0].length ?? 0;
  const coreEnd = text.value.length - trailing;
  const core = text.value.slice(leading, coreEnd);
  if (!ancestorSpan || core.length === 0) {
    return;
  }

  const ancestorSource = source.slice(ancestorSpan.start, ancestorSpan.end);
  const first = ancestorSource.indexOf(core);
  if (first < 0 || ancestorSource.indexOf(core, first + core.length) >= 0) {
    return;
  }

  const alignment = alignRenderedText(
    ancestorSource.slice(first, first + core.length),
    core,
    ancestorSpan.start + first,
    { decodeMarkdown: true }
  );
  if (alignment.error) {
    return;
  }

  const boundaries: Array<number | null> = new Array(text.value.length + 1).fill(
    null
  );
  alignment.boundaries.forEach((boundary, index) => {
    boundaries[leading + index] = boundary;
  });
  const leaf: LeafMapping = {
    id: nextLeafId(),
    text: text.value,
    boundaries
  };
  leaves.set(leaf.id, leaf);
  annotations.set(text, leaf);
}

function annotateTree(
  source: string,
  root: Root
): Pick<RenderModel, "leaves" | "annotations"> {
  const leaves = new Map<string, LeafMapping>();
  const annotations = new WeakMap<Text, LeafMapping>();
  let leafNumber = 0;
  const nextLeafId = (): string => `leaf-${++leafNumber}`;

  const visit = (
    node: Nodes,
    parent?: Element,
    positionedAncestor?: Element
  ): void => {
    if (node.type === "element" && node.tagName === "code") {
      annotateCodeElement(
        source,
        node,
        parent?.tagName === "pre",
        nextLeafId,
        leaves,
        annotations
      );
      return;
    }

    if (node.type === "text") {
      const span = positionedSpan(node);
      if (!span) {
        annotateSyntheticText(
          source,
          node,
          positionedAncestor,
          nextLeafId,
          leaves,
          annotations
        );
        return;
      }
      const alignment = alignRenderedText(
        source.slice(span.start, span.end),
        node.value,
        span.start,
        { decodeMarkdown: true }
      );
      if (alignment.error) {
        return;
      }
      const leaf: LeafMapping = {
        id: nextLeafId(),
        text: node.value,
        boundaries: alignment.boundaries
      };
      leaves.set(leaf.id, leaf);
      annotations.set(node, leaf);
      return;
    }

    if ("children" in node) {
      const nextPositionedAncestor =
        node.type === "element" && positionedSpan(node)
          ? node
          : positionedAncestor;
      for (const child of node.children) {
        visit(
          child,
          node.type === "element" ? node : parent,
          nextPositionedAncestor
        );
      }
    }
  };

  visit(root);
  return { leaves, annotations };
}

export async function buildRenderModel(source: string): Promise<RenderModel> {
  const processor = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkRehype, { allowDangerousHtml: true })
    .use(rehypeRaw)
    .use(rehypeSanitize)
    .use(rehypeHighlight, { detect: false });

  const markdownTree = processor.parse(source);
  const tree = (await processor.run(markdownTree)) as Root;
  const annotated = annotateTree(source, tree);

  return {
    source,
    tree,
    ...annotated
  };
}

function elementProperties(properties: Element["properties"]): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(properties)) {
    if (name === "className" && Array.isArray(value)) {
      result.class = value.join(" ");
    } else {
      result[name] = value;
    }
  }
  return result;
}

const voidElements = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input"]);

function renderNode(model: RenderModel, node: Nodes, key: string): ComponentChild {
  if (node.type === "text") {
    const leaf = model.annotations.get(node);
    if (!leaf) {
      return node.value;
    }
    return h(
      "span",
      {
        key,
        "data-md-leaf": leaf.id
      },
      node.value
    );
  }

  if (node.type === "element") {
    const properties = {
      key,
      ...elementProperties(node.properties)
    };
    if (voidElements.has(node.tagName)) {
      return h(node.tagName, properties);
    }
    return h(
      node.tagName,
      properties,
      node.children.map((child, index) =>
        renderNode(model, child, `${key}.${index}`)
      )
    );
  }

  if (node.type === "root") {
    return node.children.map((child, index) =>
      renderNode(model, child, `${key}.${index}`)
    );
  }

  return null;
}

export function SpikeDocument({
  model,
  generation
}: {
  model: RenderModel;
  generation: number;
}): ComponentChild {
  return h(
    "main",
    {
      id: "document",
      key: generation,
      "data-generation": generation
    },
    renderNode(model, model.tree, "root")
  );
}
