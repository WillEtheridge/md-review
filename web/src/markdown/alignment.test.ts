import { describe, expect, it } from "vitest";

import {
  alignRenderedText,
  fencedCodeContentSpan,
  inlineCodeContentSpan,
  utf16OffsetToUtf8,
  utf8OffsetToUtf16
} from "./alignment";

describe("alignRenderedText", () => {
  it("maps plain Unicode without allowing a surrogate-pair interior boundary", () => {
    const raw = "A😀B";
    const result = alignRenderedText(raw, raw, 10, {
      decodeMarkdown: true
    });

    expect(result.error).toBeUndefined();
    expect(result.boundaries).toEqual([10, 11, null, 13, 14]);
  });

  it("maps escaped punctuation and entities to complete source tokens", () => {
    const escaped = alignRenderedText("\\*asterisk\\*", "*asterisk*", 20, {
      decodeMarkdown: true
    });
    const entities = alignRenderedText("&amp; &#x1F642;", "& 🙂", 0, {
      decodeMarkdown: true
    });

    expect(escaped.boundaries[0]).toBe(20);
    expect(escaped.boundaries[1]).toBe(22);
    expect(escaped.boundaries.at(-1)).toBe(32);
    expect(entities.boundaries).toEqual([0, 5, 6, null, 15]);
  });

  it("retains CRLF while rejecting its interior boundary", () => {
    const result = alignRenderedText("first\r\nsecond", "first\r\nsecond", 5, {
      decodeMarkdown: true
    });

    expect(result.error).toBeUndefined();
    expect(result.boundaries[5]).toBe(10);
    expect(result.boundaries[6]).toBeNull();
    expect(result.boundaries[7]).toBe(12);
  });

  it("refuses rendered text that cannot be derived from the source span", () => {
    const result = alignRenderedText("source", "different", 0, {
      decodeMarkdown: true
    });

    expect(result.error).toBeDefined();
    expect(result.boundaries.every((boundary) => boundary === null)).toBe(true);
  });
});

describe("code content spans", () => {
  it("removes inline-code delimiters and CommonMark padding", () => {
    expect(inlineCodeContentSpan("`` a ` tick ``", 0, 14)).toEqual({
      raw: "a ` tick",
      start: 3
    });
  });

  it("finds exact fenced-code content after the info string", () => {
    const source = "```js\nconst x = 1;\n```\n";
    expect(fencedCodeContentSpan(source, 0, source.length - 1, "const x = 1;\n")).toEqual({
      raw: "const x = 1;\n",
      start: 6
    });
  });
});

describe("UTF-16 and UTF-8 offsets", () => {
  const source = "Aé😀中";

  it("round-trips every valid code-point boundary", () => {
    for (const utf16 of [0, 1, 2, 4, 5]) {
      const utf8 = utf16OffsetToUtf8(source, utf16);
      expect(utf8OffsetToUtf16(source, utf8)).toBe(utf16);
    }
  });

  it("rejects offsets inside a surrogate pair or UTF-8 sequence", () => {
    expect(() => utf16OffsetToUtf8(source, 3)).toThrow(/surrogate/u);
    expect(() => utf8OffsetToUtf16(source, 2)).toThrow(/splits/u);
  });
});
