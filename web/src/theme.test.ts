import { describe, expect, it } from "vitest";

import {
  applyThemeMode,
  initialiseTheme,
  persistThemeMode,
  readThemeMode,
  THEME_STORAGE_KEY
} from "./theme";

function themeRoot(): { dataset: { theme?: string } } {
  return { dataset: {} };
}

describe("theme preference", () => {
  it.each(["light", "dark", "system"] as const)("reads and applies %s", (mode) => {
    const root = themeRoot();
    const selected = initialiseTheme(root, {
      getItem: (key) => (key === THEME_STORAGE_KEY ? mode : null)
    });

    expect(selected).toBe(mode);
    expect(root.dataset.theme).toBe(mode);
  });

  it.each([null, "", "auto", "LIGHT"])("falls back to system for %j", (value) => {
    expect(readThemeMode({ getItem: () => value })).toBe("system");
  });

  it("falls back to system when storage cannot be read", () => {
    expect(
      readThemeMode({
        getItem: () => {
          throw new Error("storage denied");
        }
      })
    ).toBe("system");
  });

  it("applies a choice even when storage cannot persist it", () => {
    const root = themeRoot();
    applyThemeMode(root, "dark");

    expect(() => {
      persistThemeMode(
        {
          setItem: () => {
            throw new Error("storage denied");
          }
        },
        "dark"
      );
    }).not.toThrow();
    expect(root.dataset.theme).toBe("dark");
  });

  it("persists only the selected mode under the frozen key", () => {
    const writes: Array<[string, string]> = [];
    persistThemeMode(
      {
        setItem: (key, value) => {
          writes.push([key, value]);
        }
      },
      "system"
    );

    expect(writes).toEqual([[THEME_STORAGE_KEY, "system"]]);
  });
});
