import { describe, expect, it } from "vitest";

import { apiRequestError } from "./api.test";
import { documentFailure } from "./document-state";

describe("documentFailure", () => {
  it.each([
    ["documentTooLarge", "tooLarge"],
    ["documentInvalidUtf8", "invalidUtf8"],
    ["documentNotFound", "removed"],
    ["documentUnavailable", "error"]
  ] as const)("maps %s to the %s document state", (code, status) => {
    expect(documentFailure(apiRequestError(code))).toEqual({ status });
  });

  it("maps transport and protocol failures to a general read state", () => {
    expect(documentFailure(new TypeError("network unavailable"))).toEqual({
      status: "error"
    });
  });
});
