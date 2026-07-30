import type { Element, Nodes, Root, Text } from "hast";
import { h, type ComponentChild } from "preact";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema, type Options as SanitizeSchema } from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";

import { classifyLink } from "../link-policy";
import { ImageAsset } from "../images/ImageAsset";
import { alignRenderedText, fencedCodeContentSpan, inlineCodeContentSpan } from "./alignment";
import type { ImageDescriptor, LeafMapping, RenderModel } from "./types";

export interface DocumentNavigation {
  path: string;
  fragment: string | null;
}

interface RenderContext {
  currentDocumentPath: string;
  indexedDocumentPaths: ReadonlySet<string>;
  onNavigate: (destination: DocumentNavigation) => void;
}

type SanitizeAttributes = NonNullable<SanitizeSchema["attributes"]>;
type SanitizeProperty = SanitizeAttributes[string][number];

const sourceAuthoredAccessibilityProperties = new Set([
  "accessKey",
  "ariaDescribedBy",
  "ariaLabel",
  "ariaLabelledBy",
  "tabIndex"
]);

function propertyName(property: SanitizeProperty): string {
  return typeof property === "string" ? property : property[0];
}

function markdownSanitizeAttributes(): SanitizeAttributes {
  const attributes: SanitizeAttributes = {};
  for (const [tagName, properties] of Object.entries(defaultSchema.attributes ?? {})) {
    attributes[tagName] = properties.filter(
      (property) => !sourceAuthoredAccessibilityProperties.has(propertyName(property))
    );
  }
  return attributes;
}

const markdownSanitizeSchema: SanitizeSchema = {
  ...defaultSchema,
  attributes: markdownSanitizeAttributes()
};

function textContent(node: Nodes): string {
  if (node.type === "text") {
    return node.value;
  }
  if ("children" in node) {
    return node.children.map(textContent).join("");
  }
  return "";
}

function positionedSpan(node: {
  position?: Nodes["position"];
}): { start: number; end: number } | undefined {
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

  const alignment = alignRenderedText(contentSpan.raw, rendered, contentSpan.start, {
    decodeMarkdown: false,
    normaliseLineEndingsToSpace: !fenced
  });
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

  const boundaries = Array<number | null>(text.value.length + 1).fill(null);
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

function annotateTree(source: string, root: Root): Pick<RenderModel, "leaves" | "annotations"> {
  const leaves = new Map<string, LeafMapping>();
  const annotations = new WeakMap<Text, LeafMapping>();
  let leafNumber = 0;
  const nextLeafId = (): string => `leaf-${String(++leafNumber)}`;

  const visit = (node: Nodes, parent?: Element, positionedAncestor?: Element): void => {
    if (node.type === "element" && node.tagName === "code") {
      annotateCodeElement(source, node, parent?.tagName === "pre", nextLeafId, leaves, annotations);
      return;
    }

    if (node.type === "text") {
      const span = positionedSpan(node);
      if (!span) {
        annotateSyntheticText(source, node, positionedAncestor, nextLeafId, leaves, annotations);
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
        node.type === "element" && positionedSpan(node) ? node : positionedAncestor;
      for (const child of node.children) {
        visit(child, node.type === "element" ? node : parent, nextPositionedAncestor);
      }
    }
  };

  visit(root);
  return { leaves, annotations };
}

const inertMediaTags = new Set([
  "audio",
  "embed",
  "iframe",
  "img",
  "object",
  "picture",
  "source",
  "video"
]);

function imageAltText(node: Nodes): string {
  if (node.type !== "element") {
    return "";
  }
  const ownAlt = node.properties.alt;
  if (typeof ownAlt === "string") {
    return ownAlt;
  }
  for (const child of node.children) {
    const nested = imageAltText(child);
    if (nested.length > 0) {
      return nested;
    }
  }
  return "";
}

function makeMediaInert(element: Element): void {
  const isImage = element.tagName === "img" || element.tagName === "picture";
  const alt = imageAltText(element);
  const label = isImage
    ? alt.length > 0
      ? `Image: ${alt}`
      : "Image omitted"
    : "Embedded media omitted";

  // No source-authored media URL survives into the render tree. Milestone 5
  // may replace this only after authenticated contained asset loading exists.
  element.tagName = "span";
  element.properties = {
    className: ["markdown-media-placeholder"],
    role: "img",
    ariaLabel: label
  };
  element.children = [
    {
      type: "text",
      value: label
    }
  ];
}

function headingSlug(value: string): string {
  return value
    .normalize("NFKC")
    .toLowerCase()
    .trim()
    .replace(/[^\p{Letter}\p{Number}\s_-]/gu, "")
    .replace(/\s+/gu, "-");
}

function taskAccessibleName(element: Element, parent: Element | undefined): string {
  const visibleLabel = parent ? textContent(parent).replace(/\s+/gu, " ").trim() : "";
  if (visibleLabel.length > 0) {
    return Array.from(visibleLabel).slice(0, 160).join("");
  }
  return element.properties.checked === true ? "Completed task" : "Incomplete task";
}

function relativeImageDescriptor(source: string, element: Element): ImageDescriptor | undefined {
  const span = positionedSpan(element);
  const reference = element.properties.src;
  if (
    !span ||
    !source.slice(span.start, span.end).trimStart().startsWith("![") ||
    typeof reference !== "string" ||
    reference === "" ||
    reference.startsWith("/") ||
    reference.startsWith("//") ||
    reference.includes("\\") ||
    reference.includes("?") ||
    reference.includes("#") ||
    reference.includes("\0") ||
    /^[A-Za-z][A-Za-z0-9+.-]*:/u.test(reference)
  ) {
    return undefined;
  }
  const title = element.properties.title;
  return {
    reference,
    alt: imageAltText(element),
    ...(typeof title === "string" && title !== "" ? { title } : {})
  };
}

function secureRenderTree(source: string, root: Root): WeakMap<Element, ImageDescriptor> {
  const slugCounts = new Map<string, number>();
  const images = new WeakMap<Element, ImageDescriptor>();

  const visit = (node: Nodes, parent?: Element): void => {
    if (node.type !== "element") {
      if ("children" in node) {
        node.children.forEach((child) => {
          visit(child, parent);
        });
      }
      return;
    }

    if (inertMediaTags.has(node.tagName)) {
      if (node.tagName === "img") {
        const descriptor = relativeImageDescriptor(source, node);
        if (descriptor) {
          images.set(node, descriptor);
        }
      }
      makeMediaInert(node);
      return;
    }

    if (node.tagName === "input" && node.properties.type === "checkbox") {
      node.properties.ariaLabel = taskAccessibleName(node, parent);
    }

    if (/^h[1-6]$/u.test(node.tagName) && typeof node.properties.id !== "string") {
      const base = headingSlug(textContent(node));
      if (base.length > 0) {
        const duplicateNumber = slugCounts.get(base) ?? 0;
        slugCounts.set(base, duplicateNumber + 1);
        node.properties.id = duplicateNumber === 0 ? base : `${base}-${String(duplicateNumber)}`;
      }
    }

    node.children.forEach((child) => {
      visit(child, node);
    });
  };

  visit(root);
  return images;
}

export async function buildRenderModel(source: string): Promise<RenderModel> {
  const processor = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkRehype, { allowDangerousHtml: true })
    .use(rehypeRaw)
    // Raw Markdown HTML is untrusted presentation. In particular it cannot
    // inject focus order or replace visible labels with misleading ARIA.
    .use(rehypeSanitize, markdownSanitizeSchema);

  const markdownTree = processor.parse(source);
  const tree = await processor.run(markdownTree);
  const images = secureRenderTree(source, tree);
  const annotated = annotateTree(source, tree);

  return {
    source,
    tree,
    images,
    ...annotated
  };
}

const blockedURLProperties = new Set([
  "action",
  "background",
  "cite",
  "data",
  "formAction",
  "href",
  "poster",
  "src",
  "srcSet"
]);

function elementProperties(properties: Element["properties"]): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(properties)) {
    if (blockedURLProperties.has(name)) {
      continue;
    }
    if (name === "className" && Array.isArray(value)) {
      result.class = value.join(" ");
    } else {
      result[name] = value;
    }
  }
  return result;
}

const voidElements = new Set(["area", "base", "br", "col", "hr", "input"]);

function renderLink(
  model: RenderModel,
  node: Element,
  key: string,
  context: RenderContext
): ComponentChild {
  const children = node.children.map((child, index) =>
    renderNode(model, child, `${key}.${String(index)}`, context)
  );
  const href = node.properties.href;
  if (typeof href !== "string") {
    return h("span", { key, class: "markdown-link-inert" }, children);
  }

  const policy = classifyLink(href, context.currentDocumentPath, context.indexedDocumentPaths);
  if (policy.kind === "external") {
    return h(
      "a",
      {
        key,
        href: policy.href,
        target: "_blank",
        rel: "noopener noreferrer"
      },
      children
    );
  }

  if (policy.kind === "fragment") {
    return h("a", { key, href: policy.href }, children);
  }

  if (policy.kind === "document") {
    const handleNavigate = (event: MouseEvent): void => {
      event.preventDefault();
      context.onNavigate({
        path: policy.path,
        fragment: policy.fragment
      });
    };
    return h(
      "a",
      {
        key,
        href: policy.href,
        onClick: handleNavigate
      },
      children
    );
  }

  return h("span", { key, class: "markdown-link-inert" }, children);
}

function renderNode(
  model: RenderModel,
  node: Nodes,
  key: string,
  context: RenderContext
): ComponentChild {
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
    const image = model.images.get(node);
    if (image) {
      return h(ImageAsset, {
        key,
        documentPath: context.currentDocumentPath,
        reference: image.reference,
        alt: image.alt,
        ...(image.title ? { title: image.title } : {})
      });
    }

    if (node.tagName === "a") {
      return renderLink(model, node, key, context);
    }

    // This is a second boundary behind the tree transform: even a future
    // parser change cannot turn a media node into a browser request.
    if (inertMediaTags.has(node.tagName)) {
      return h("span", { key, class: "markdown-media-placeholder" }, "Media omitted");
    }

    const properties = {
      ...elementProperties(node.properties)
    };
    const children = node.children.map((child, index) =>
      renderNode(model, child, `${key}.${String(index)}`, context)
    );
    if (node.tagName === "table") {
      return h(
        "div",
        {
          key,
          class: "markdown-overflow markdown-table-overflow",
          role: "group",
          tabIndex: 0,
          "aria-label": "Scrollable table"
        },
        h("table", properties, children)
      );
    }
    if (node.tagName === "pre") {
      return h(
        "div",
        {
          key,
          class: "markdown-overflow markdown-code-overflow",
          role: "group",
          tabIndex: 0,
          "aria-label": "Scrollable code block"
        },
        h("pre", properties, children)
      );
    }
    if (voidElements.has(node.tagName)) {
      return h(node.tagName, { key, ...properties });
    }
    return h(node.tagName, { key, ...properties }, children);
  }

  if (node.type === "root") {
    return node.children.map((child, index) =>
      renderNode(model, child, `${key}.${String(index)}`, context)
    );
  }

  return null;
}

export function MarkdownDocument({
  documentRef,
  model,
  currentDocumentPath,
  indexedDocumentPaths,
  onNavigate
}: {
  documentRef?: { current: HTMLElement | null };
  model: RenderModel;
  currentDocumentPath: string;
  indexedDocumentPaths: ReadonlySet<string>;
  onNavigate: (destination: DocumentNavigation) => void;
}): ComponentChild {
  return h(
    "article",
    {
      class: "markdown-body",
      ref: documentRef,
      "aria-label": `Document: ${currentDocumentPath}`,
      "data-document-path": currentDocumentPath
    },
    renderNode(model, model.tree, "root", {
      currentDocumentPath,
      indexedDocumentPaths,
      onNavigate
    })
  );
}
