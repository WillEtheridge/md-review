import type { TextReviewThread } from "./api";

export function orderTextThreads(threads: readonly TextReviewThread[]): TextReviewThread[] {
  return threads
    .map((thread, index) => ({
      thread,
      index,
      start:
        thread.attachment.state === "attached" ? thread.attachment.currentRange.start : Infinity
    }))
    .sort((left, right) => left.start - right.start || left.index - right.index)
    .map(({ thread }) => thread);
}
