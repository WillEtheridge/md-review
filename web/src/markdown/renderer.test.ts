import type { Element, Nodes } from "hast";
import { describe, expect, it } from "vitest";

import { buildRenderModel } from "./renderer";

function elements(node: Nodes): Element[] {
  if (node.type === "element") {
    return [node, ...node.children.flatMap(elements)];
  }
  if ("children" in node) {
    return node.children.flatMap(elements);
  }
  return [];
}

function visibleText(node: Nodes): string {
  if (node.type === "text") {
    return node.value;
  }
  if ("children" in node) {
    return node.children.map(visibleText).join("");
  }
  return "";
}

describe("buildRenderModel", () => {
  it("parses the supported GFM presentation and adds source-map leaves", async () => {
    const source = `# Heading

## Second

### Third

#### Fourth

##### Fifth

###### Sixth

Paragraph with *emphasis*, **strong**, and ~~strike~~.

> Quote
>
> > Nested quote

3. Ordered from three
4. List
   - Nested item

- [x] Task
- [ ] Pending

| Name | Value |
| :--- | ---: |
| Alice | 42 |

\`inline code\` and www.example.com.

\`\`\`js
const greeting = "hello";
\`\`\`

---
`;
    const model = await buildRenderModel(source);
    const renderedElements = elements(model.tree);
    const tags = new Set(renderedElements.map(({ tagName }) => tagName));

    for (const tag of [
      "h1",
      "h2",
      "h3",
      "h4",
      "h5",
      "h6",
      "p",
      "em",
      "strong",
      "del",
      "blockquote",
      "ol",
      "ul",
      "input",
      "table",
      "pre",
      "code",
      "hr"
    ]) {
      expect(tags.has(tag)).toBe(true);
    }
    expect(renderedElements.find(({ tagName }) => tagName === "h1")?.properties.id).toBe("heading");
    expect(renderedElements.find(({ tagName }) => tagName === "ol")?.properties.start).toBe(3);
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "input")
        .map(({ properties }) => properties.checked)
    ).toEqual([true, undefined]);
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "input")
        .every(({ properties }) => properties.disabled === true && properties.type === "checkbox")
    ).toBe(true);
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "input")
        .map(({ properties }) => properties.ariaLabel)
    ).toEqual(["Task", "Pending"]);
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "th" || tagName === "td")
        .map(({ properties }) => properties.align)
    ).toEqual(["left", "right", "left", "right"]);
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "code")
        .some(({ properties }) => String(properties.className).includes("hljs"))
    ).toBe(true);
    expect(model.leaves.size).toBeGreaterThan(10);
    expect(
      Array.from(model.leaves.values()).every(
        (leaf) => leaf.boundaries.length === leaf.text.length + 1
      )
    ).toBe(true);
  });

  it("assigns stable unique fragments to repeated generated headings", async () => {
    const model = await buildRenderModel("# Repeated\n\n## Repeated\n\n### Répeated!\n");
    const headingIDs = elements(model.tree)
      .filter(({ tagName }) => /^h[1-6]$/u.test(tagName))
      .map(({ properties }) => properties.id);

    expect(headingIDs).toEqual(["repeated", "repeated-1", "répeated"]);
  });

  it("sanitises executable raw HTML", async () => {
    const model = await buildRenderModel(
      `Before <span onclick="alert('event')">safe text</span>.

<script>alert("script")</script>

<a href="javascript:alert('url')">unsafe link</a>`
    );
    const renderedElements = elements(model.tree);
    const serializedTree = JSON.stringify(model.tree);

    expect(renderedElements.some(({ tagName }) => tagName === "script")).toBe(false);
    expect(serializedTree).not.toContain("onclick");
    expect(serializedTree).not.toContain('alert("script")');
    expect(
      renderedElements
        .filter(({ tagName }) => tagName === "a")
        .every(({ properties }) => properties.href === undefined)
    ).toBe(true);
    expect(visibleText(model.tree)).toContain("safe text");
  });

  it("strips source-authored focus order and misleading accessible names", async () => {
    const model = await buildRenderModel(`<a
  href="https://example.com"
  tabindex="7"
  accesskey="x"
  aria-label="Misleading action"
  aria-describedby="missing-description"
>Visible link</a>

<table tabindex="-1" aria-labelledby="missing-table-name">
  <tr><th>Visible heading</th></tr>
</table>`);
    const interactiveElements = elements(model.tree).filter(
      ({ tagName }) => tagName === "a" || tagName === "table"
    );

    for (const element of interactiveElements) {
      expect(element.properties).not.toHaveProperty("tabIndex");
      expect(element.properties).not.toHaveProperty("accessKey");
      expect(element.properties).not.toHaveProperty("ariaLabel");
      expect(element.properties).not.toHaveProperty("ariaDescribedBy");
      expect(element.properties).not.toHaveProperty("ariaLabelledBy");
    }
    expect(visibleText(model.tree)).toContain("Visible link");
    expect(visibleText(model.tree)).toContain("Visible heading");
  });

  it("keeps only safe Markdown image descriptors outside the serialised tree", async () => {
    const remoteSource = "https://images.invalid/remote.png?secret=one";
    const dataSource = "data:image/png;base64,SECRET_TWO";
    const localSource = "./local.png?secret=three";
    const containedSource = "../images/diagram.png";
    const rawSource = "https://images.invalid/raw.png?secret=four";
    const model = await buildRenderModel(`![Remote alt](${remoteSource})

![Data alt](${dataSource})

![Local alt](${localSource})

![Contained alt](${containedSource} "Diagram title")

<img alt="Raw alt" src="${rawSource}">`);
    const renderedElements = elements(model.tree);
    const serializedTree = JSON.stringify(model.tree);
    const placeholders = renderedElements.filter(({ properties }) =>
      Array.isArray(properties.className)
        ? properties.className.includes("markdown-media-placeholder")
        : false
    );

    expect(renderedElements.some(({ tagName }) => tagName === "img")).toBe(false);
    expect(placeholders).toHaveLength(5);
    expect(placeholders[0]?.properties).toMatchObject({
      role: "img",
      ariaLabel: "Image: Remote alt"
    });
    expect(visibleText(model.tree)).toContain("Image: Remote alt");
    expect(visibleText(model.tree)).toContain("Image: Raw alt");
    const descriptors = placeholders.flatMap((placeholder) => {
      const descriptor = model.images.get(placeholder);
      return descriptor ? [descriptor] : [];
    });
    expect(descriptors).toEqual([
      {
        reference: containedSource,
        alt: "Contained alt",
        title: "Diagram title"
      }
    ]);
    for (const source of [remoteSource, dataSource, localSource, containedSource, rawSource]) {
      expect(serializedTree).not.toContain(source);
    }
  });

  it("retains exact mappings for entities, escapes, inline code, and highlighted code", async () => {
    const model = await buildRenderModel(`Escaped \\*asterisk\\* and &amp; &#x1F642;.

Code: \`a   b\`.

\`\`\`js
const value = "mapped";
\`\`\`
`);
    const leaves = Array.from(model.leaves.values());

    expect(leaves.some((leaf) => leaf.text.includes("*asterisk*"))).toBe(true);
    expect(leaves.some((leaf) => leaf.text.includes("& 🙂"))).toBe(true);
    expect(leaves.some((leaf) => leaf.text === "a   b")).toBe(true);
    expect(
      leaves
        .filter((leaf) => /const|value|mapped/u.test(leaf.text))
        .every((leaf) => leaf.boundaries.some(Number.isInteger))
    ).toBe(true);
  });
});
