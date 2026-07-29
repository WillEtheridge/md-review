import { describe, expect, it } from "vitest";

import type { TextReviewThread } from "./api";
import { orderTextThreads } from "./review-order";

function attachedThread(id: string, start: number): TextReviewThread {
  return {
    id,
    status: "open",
    messages: [],
    anchor: {
      type: "text",
      range: { start, end: start + 1 },
      source: "x",
      text: "x"
    },
    attachment: {
      state: "attached",
      currentRange: { start, end: start + 1 }
    }
  };
}

function detachedThread(id: string): TextReviewThread {
  return {
    id,
    status: "open",
    messages: [],
    anchor: {
      type: "text",
      range: { start: 0, end: 1 },
      source: "x",
      text: "x"
    },
    attachment: { state: "detached" }
  };
}

describe("orderTextThreads", () => {
  it("orders attached threads by current source position and retains detached threads last", () => {
    const ordered = orderTextThreads([
      detachedThread("detached"),
      attachedThread("later", 40),
      attachedThread("first-at-position", 8),
      attachedThread("second-at-position", 8)
    ]);

    expect(ordered.map((thread) => thread.id)).toEqual([
      "first-at-position",
      "second-at-position",
      "later",
      "detached"
    ]);
  });
});
