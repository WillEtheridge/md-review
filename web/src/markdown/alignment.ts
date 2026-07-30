import { decodeNamedCharacterReference } from "decode-named-character-reference";

interface VisibleToken {
  rawStart: number;
  rawEnd: number;
  value: string;
}

interface AlignmentOptions {
  /** Decode Markdown escapes and entities before aligning rendered text. */
  decodeMarkdown?: boolean;
  /** Treat source line endings as the single spaces rendered by inline code. */
  normaliseLineEndingsToSpace?: boolean;
}

/** Rendered-to-source UTF-16 boundary map, or an explanation of divergence. */
export interface BoundaryAlignment {
  boundaries: Array<number | null>;
  error?: string;
}

const asciiPunctuation = /^[!-/:-@[-`{-~]$/u;
const entityPattern = /^&(?:#(?:[xX][0-9A-Fa-f]+|[0-9]+)|[A-Za-z][A-Za-z0-9]+);/u;

function decodeEntity(entity: string): string | undefined {
  if (entity.startsWith("&#x") || entity.startsWith("&#X")) {
    const value = Number.parseInt(entity.slice(3, -1), 16);
    return validCodePoint(value) ? String.fromCodePoint(value) : "\uFFFD";
  }

  if (entity.startsWith("&#")) {
    const value = Number.parseInt(entity.slice(2, -1), 10);
    return validCodePoint(value) ? String.fromCodePoint(value) : "\uFFFD";
  }

  return decodeNamedCharacterReference(entity.slice(1, -1)) || undefined;
}

function validCodePoint(value: number): boolean {
  return (
    Number.isInteger(value) &&
    value > 0 &&
    value <= 0x10ffff &&
    !(value >= 0xd800 && value <= 0xdfff)
  );
}

function rawTokens(raw: string, options: AlignmentOptions): VisibleToken[] {
  const tokens: VisibleToken[] = [];
  let index = 0;

  while (index < raw.length) {
    if (raw.startsWith("\r\n", index)) {
      tokens.push({
        rawStart: index,
        rawEnd: index + 2,
        value: options.normaliseLineEndingsToSpace ? " " : "\r\n"
      });
      index += 2;
      continue;
    }

    const codePoint = raw.codePointAt(index);
    if (codePoint === undefined) {
      break;
    }

    const character = String.fromCodePoint(codePoint);

    if (character === "\n" && options.normaliseLineEndingsToSpace) {
      tokens.push({ rawStart: index, rawEnd: index + 1, value: " " });
      index += 1;
      continue;
    }

    if (options.decodeMarkdown && character === "\\") {
      const nextCodePoint = raw.codePointAt(index + 1);
      if (nextCodePoint !== undefined) {
        const next = String.fromCodePoint(nextCodePoint);
        if (asciiPunctuation.test(next)) {
          tokens.push({
            rawStart: index,
            rawEnd: index + 1 + next.length,
            value: next
          });
          index += 1 + next.length;
          continue;
        }
      }
    }

    if (options.decodeMarkdown && character === "&") {
      const match = raw.slice(index).match(entityPattern)?.[0];
      if (match) {
        const decoded = decodeEntity(match);
        if (decoded !== undefined) {
          tokens.push({
            rawStart: index,
            rawEnd: index + match.length,
            value: decoded
          });
          index += match.length;
          continue;
        }
      }
    }

    tokens.push({
      rawStart: index,
      rawEnd: index + character.length,
      value: character
    });
    index += character.length;
  }

  return tokens;
}

function setBoundary(
  boundaries: Array<number | null | undefined>,
  renderedOffset: number,
  sourceOffset: number
): void {
  // Multiple source tokens can produce the same rendered boundary (or a parser
  // can synthesize text). Nulling the boundary prevents the caller from making
  // a false precision claim about the authored range.
  const existing = boundaries[renderedOffset];
  if (existing === undefined) {
    boundaries[renderedOffset] = sourceOffset;
  } else if (existing !== sourceOffset) {
    boundaries[renderedOffset] = null;
  }
}

/**
 * Aligns parser-rendered text with its authored source span. Returns null
 * boundaries where normalization is not one-to-one and an error when the
 * renderer diverges from the source slice.
 */
export function alignRenderedText(
  raw: string,
  rendered: string,
  absoluteSourceStart: number,
  options: AlignmentOptions = { decodeMarkdown: true }
): BoundaryAlignment {
  // Alignment is intentionally prefix-driven rather than a fuzzy diff. A
  // fuzzy match could attach a comment to different source bytes after parser
  // normalization, so divergence rejects the whole leaf.
  const tokens = rawTokens(raw, options);
  const boundaries = Array<number | null | undefined>(rendered.length + 1);
  let renderedOffset = 0;

  for (const token of tokens) {
    if (!rendered.startsWith(token.value, renderedOffset)) {
      return {
        boundaries: Array<number | null>(rendered.length + 1).fill(null),
        error: `Rendered text diverged at UTF-16 offset ${String(renderedOffset)}`
      };
    }

    setBoundary(boundaries, renderedOffset, absoluteSourceStart + token.rawStart);
    renderedOffset += token.value.length;
    setBoundary(boundaries, renderedOffset, absoluteSourceStart + token.rawEnd);

    for (
      let interior = renderedOffset - token.value.length + 1;
      interior < renderedOffset;
      interior += 1
    ) {
      boundaries[interior] = null;
    }
  }

  if (renderedOffset !== rendered.length) {
    return {
      boundaries: Array<number | null>(rendered.length + 1).fill(null),
      error: `Rendered text has ${String(rendered.length - renderedOffset)} unmatched code units`
    };
  }

  return {
    boundaries: boundaries.map((value) => value ?? null)
  };
}

/** Returns the authored content span represented by one inline-code element. */
export function inlineCodeContentSpan(
  source: string,
  sourceStart: number,
  sourceEnd: number
): { raw: string; start: number } | undefined {
  const raw = source.slice(sourceStart, sourceEnd);
  const opening = raw.match(/^`+/u)?.[0];
  if (!opening || !raw.endsWith(opening) || raw.length < opening.length * 2) {
    return undefined;
  }

  let contentStart = opening.length;
  let contentEnd = raw.length - opening.length;
  const content = raw.slice(contentStart, contentEnd);
  const normalised = content.replace(/\r\n?|\n/gu, " ");

  if (normalised.startsWith(" ") && normalised.endsWith(" ") && /[^ ]/u.test(normalised)) {
    if (raw.startsWith("\r\n", contentStart)) {
      contentStart += 2;
    } else {
      contentStart += 1;
    }

    if (raw.slice(0, contentEnd).endsWith("\r\n")) {
      contentEnd -= 2;
    } else {
      contentEnd -= 1;
    }
  }

  return {
    raw: raw.slice(contentStart, contentEnd),
    start: sourceStart + contentStart
  };
}

/** Returns the authored body span represented by one fenced code element. */
export function fencedCodeContentSpan(
  source: string,
  sourceStart: number,
  sourceEnd: number,
  rendered: string
): { raw: string; start: number } | undefined {
  const raw = source.slice(sourceStart, sourceEnd);
  const firstLineEnding = raw.search(/\r?\n/u);
  if (firstLineEnding < 0) {
    return undefined;
  }

  const bodyStart = firstLineEnding + (raw.startsWith("\r\n", firstLineEnding) ? 2 : 1);
  const exactIndex = raw.indexOf(rendered, bodyStart);
  if (exactIndex >= bodyStart) {
    return {
      raw: raw.slice(exactIndex, exactIndex + rendered.length),
      start: sourceStart + exactIndex
    };
  }

  const closingLine = raw.search(/\r?\n(?: {0,3})(?:`{3,}|~{3,})[ \t]*$/u);
  if (closingLine < 0) {
    return undefined;
  }

  return {
    raw: raw.slice(bodyStart, closingLine + 1),
    start: sourceStart + bodyStart
  };
}

/** Converts an exact JavaScript UTF-16 source boundary to a UTF-8 byte offset. */
export function utf16OffsetToUtf8(source: string, offset: number): number {
  // DOM Range offsets count UTF-16 code units; sidecar anchors count UTF-8
  // bytes. Rejecting surrogate interiors keeps both representations on code
  // point boundaries instead of silently rounding a user selection.
  if (offset < 0 || offset > source.length) {
    throw new RangeError("UTF-16 offset is outside the source");
  }

  if (
    offset > 0 &&
    offset < source.length &&
    /[\uD800-\uDBFF]/u.test(source[offset - 1] ?? "") &&
    /[\uDC00-\uDFFF]/u.test(source[offset] ?? "")
  ) {
    throw new RangeError("UTF-16 offset splits a surrogate pair");
  }

  return new TextEncoder().encode(source.slice(0, offset)).length;
}

/** Converts an exact UTF-8 byte boundary to a JavaScript UTF-16 source offset. */
export function utf8OffsetToUtf16(source: string, byteOffset: number): number {
  // Walk complete Unicode code points until the exact byte boundary is reached.
  // A partial code point is invalid even if it would be possible to slice the
  // JavaScript string at a nearby UTF-16 index.
  if (byteOffset < 0) {
    throw new RangeError("UTF-8 offset is outside the source");
  }

  const encoder = new TextEncoder();
  let utf16Offset = 0;
  let bytes = 0;

  while (utf16Offset < source.length && bytes < byteOffset) {
    const codePoint = source.codePointAt(utf16Offset);
    if (codePoint === undefined) {
      break;
    }
    const character = String.fromCodePoint(codePoint);
    bytes += encoder.encode(character).length;
    utf16Offset += character.length;
  }

  if (bytes !== byteOffset) {
    throw new RangeError("UTF-8 offset splits a code point or exceeds the source");
  }

  return utf16Offset;
}
