import { ApiRequestError } from "./api";

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

export function documentFailure(error: unknown): DocumentFailure {
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
