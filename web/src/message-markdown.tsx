import type { Element, Nodes, Root } from "hast";
import { h, type ComponentChild } from "preact";
import { useMemo } from "preact/hooks";
import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import remarkRehype from "remark-rehype";
import { unified } from "unified";

const renderedTags = new Set([
  "a",
  "blockquote",
  "br",
  "code",
  "del",
  "em",
  "li",
  "ol",
  "p",
  "pre",
  "strong",
  "ul"
]);

const discardedTags = new Set(["hr", "img", "input"]);

export function buildMessageTree(source: string): Root {
  const processor = unified().use(remarkParse).use(remarkGfm).use(remarkRehype);
  return processor.runSync(processor.parse(source));
}

function safeLink(href: unknown): string | undefined {
  if (typeof href !== "string") {
    return undefined;
  }
  let parsed: URL;
  try {
    parsed = new URL(href);
  } catch {
    return undefined;
  }
  if (
    parsed.protocol !== "http:" &&
    parsed.protocol !== "https:" &&
    parsed.protocol !== "mailto:"
  ) {
    return undefined;
  }
  return parsed.href;
}

function renderChildren(node: Element | Root, key: string): ComponentChild[] {
  return node.children.map((child, index) => renderMessageNode(child, `${key}.${String(index)}`));
}

function plainContainerTag(node: Element): "div" | "p" | "span" {
  if (/^h[1-6]$/u.test(node.tagName)) {
    return "p";
  }
  if (
    node.tagName === "table" ||
    node.tagName === "thead" ||
    node.tagName === "tbody" ||
    node.tagName === "tr"
  ) {
    return "div";
  }
  return "span";
}

function renderMessageNode(node: Nodes, key: string): ComponentChild {
  if (node.type === "text") {
    return node.value;
  }
  if (node.type === "root") {
    return renderChildren(node, key);
  }
  if (node.type !== "element" || discardedTags.has(node.tagName)) {
    return null;
  }

  const children = renderChildren(node, key);
  if (node.tagName === "a") {
    const href = safeLink(node.properties.href);
    return href
      ? h("a", { key, href, target: "_blank", rel: "noopener noreferrer" }, children)
      : h("span", { key, class: "message-link-inert" }, children);
  }
  if (renderedTags.has(node.tagName)) {
    return h(node.tagName, { key }, children);
  }

  // Headings, tables and other GFM containers retain readable text without
  // exposing their richer document semantics inside a review message.
  return h(plainContainerTag(node), { key, class: "message-reduced-content" }, children);
}

export function MessageMarkdown({ source }: { source: string }): ComponentChild {
  const tree = useMemo(() => buildMessageTree(source), [source]);
  return h("div", { class: "message-markdown" }, renderMessageNode(tree, "message"));
}
