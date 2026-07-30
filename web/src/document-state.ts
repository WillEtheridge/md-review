import { ApiRequestError } from "./api";

/** Stable UI states for a document that could not be opened. */
export type DocumentFailure =
  | {
      status: "tooLarge";
    }
  | {
      status: "invalidUtf8";
    }
  | {
      status: "removed";
    }
  | {
      status: "error";
    };

/** Converts an API failure into the small document-state union rendered by App. */
export function documentFailure(error: unknown): DocumentFailure {
  // Keep server distinctions that change the recovery UI, while collapsing
  // transport and unexpected failures into a safe generic state.
  if (!(error instanceof ApiRequestError)) {
    return { status: "error" };
  }

  if (error.code === "documentTooLarge") {
    return { status: "tooLarge" };
  }
  if (error.code === "documentInvalidUtf8") {
    return { status: "invalidUtf8" };
  }
  if (error.code === "documentNotFound") {
    return { status: "removed" };
  }
  return { status: "error" };
}
