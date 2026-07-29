import type { Nodes } from "hast";
import { describe, expect, it } from "vitest";

import { buildMessageTree } from "./message-markdown";

function elementTags(node: Nodes): string[] {
  if (node.type === "element") {
    return [node.tagName, ...node.children.flatMap(elementTags)];
  }
  if ("children" in node) {
    return node.children.flatMap(elementTags);
  }
  return [];
}

describe("reduced message Markdown", () => {
  it("parses the allowed prose and code features without activating raw HTML", () => {
    const tree = buildMessageTree(
      "**Strong** and ~~removed~~ with `code`.\n\n```ts\nconst value = 1;\n```\n\n<img src=x onerror=alert(1)>"
    );

    expect(elementTags(tree)).toEqual(["p", "strong", "del", "code", "pre", "code"]);
  });

  it("keeps images as discardable nodes and does not preserve task controls as authored HTML", () => {
    const tree = buildMessageTree("![secret](file:///etc/passwd)\n\n- [x] done");
    const tags = elementTags(tree);

    expect(tags).toContain("img");
    expect(tags).toContain("input");
  });
});
