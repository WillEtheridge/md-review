import { describe, expect, it } from "vitest";

import { classifyLink } from "./link-policy";

const documents = new Set(["README.md", "docs/guide.md", "reference.md", "space name.md"]);

describe("classifyLink", () => {
  it.each([
    ["https://example.com/path", "https://example.com/path"],
    ["http://example.com/path", "http://example.com/path"],
    ["mailto:reviewer@example.com", "mailto:reviewer@example.com"]
  ])("allows safe external link %s", (href, expected) => {
    expect(classifyLink(href, "README.md", documents)).toEqual({
      kind: "external",
      href: expected
    });
  });

  it("allows current-document fragments", () => {
    expect(classifyLink("#overview", "README.md", documents)).toEqual({
      kind: "fragment",
      href: "#overview",
      fragment: "overview"
    });
  });

  it.each([
    ["docs/guide.md#usage", "README.md", "docs/guide.md", "usage"],
    ["../reference.md", "docs/guide.md", "reference.md", null],
    ["space%20name.md", "README.md", "space name.md", null]
  ])("resolves indexed Markdown link %s", (href, current, path, fragment) => {
    expect(classifyLink(href, current, documents)).toEqual({
      kind: "document",
      href,
      path,
      fragment
    });
  });

  it.each([
    "javascript:alert(1)",
    "data:text/html,unsafe",
    "//example.com/path",
    "/api/state",
    "../../escape.md",
    "missing.md",
    "guide.txt",
    "docs/guide.md?download=true",
    "https://example.com/\nheader"
  ])("makes unsafe or unindexed link %s inert", (href) => {
    expect(classifyLink(href, "README.md", documents)).toEqual({
      kind: "inert"
    });
  });
});
