import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { buildRenderModel } from "../../src/markdown";

const fixtures = resolve(import.meta.dirname, "../../public/fixtures");

async function fixture(name: string): Promise<string> {
  return readFile(resolve(fixtures, name), "utf8");
}

describe("buildRenderModel", () => {
  it("annotates direct, entity, escaped, and inline-code text leaves", async () => {
    const model = await buildRenderModel(await fixture("inline.md"));
    const leaves = Array.from(model.leaves.values());

    expect(leaves.some((leaf) => leaf.text.includes("emphasis"))).toBe(true);
    expect(leaves.some((leaf) => leaf.text.includes("*asterisk*"))).toBe(true);
    expect(leaves.some((leaf) => leaf.text.includes("& 🙂"))).toBe(true);
    expect(leaves.some((leaf) => leaf.text === "a   b")).toBe(true);
    expect(leaves.some((leaf) => leaf.text === "multiline code")).toBe(true);
    expect(
      leaves.every((leaf) => leaf.boundaries.length === leaf.text.length + 1)
    ).toBe(true);
  });

  it("preserves source mappings across syntax-highlighting spans", async () => {
    const model = await buildRenderModel(await fixture("code.md"));
    const codeLeaves = Array.from(model.leaves.values()).filter((leaf) =>
      /const|greeting|console|log/u.test(leaf.text)
    );

    expect(codeLeaves.length).toBeGreaterThan(2);
    expect(codeLeaves.every((leaf) => leaf.boundaries.some(Number.isInteger))).toBe(
      true
    );
  });

  it("normalises CRLF rendering while retaining the CRLF source offsets", async () => {
    const model = await buildRenderModel(await fixture("line-endings-crlf.md"));
    const leaf = Array.from(model.leaves.values()).find((candidate) =>
      candidate.text.includes("First line")
    );

    expect(leaf?.text).toContain("First line\r\nsecond line.");
    const carriageReturn = leaf?.text.indexOf("\r") ?? -1;
    expect(carriageReturn).toBeGreaterThanOrEqual(0);
    expect(leaf?.boundaries[carriageReturn + 1]).toBeNull();
    expect(leaf?.boundaries[carriageReturn + 2]).toBe(
      (leaf?.boundaries[carriageReturn] ?? 0) + 2
    );
  });
});
