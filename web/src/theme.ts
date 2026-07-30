export const THEME_STORAGE_KEY = "mdreview.theme";

/** User-selected theme, with system following the OS preference. */
export type ThemeMode = "light" | "dark" | "system";

interface ThemeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

interface ThemeRoot {
  dataset: {
    theme?: string;
  };
}

/** Narrows persisted or external values to the supported theme union. */
export function isThemeMode(value: unknown): value is ThemeMode {
  return value === "light" || value === "dark" || value === "system";
}

/** Returns localStorage when available without making startup depend on it. */
export function browserThemeStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** Reads a validated theme preference, defaulting safely to system mode. */
export function readThemeMode(storage: Pick<ThemeStorage, "getItem"> | null): ThemeMode {
  // Storage is an optional enhancement. Private browsing, disabled storage, or
  // malformed old values fall back to the system theme without blocking startup.
  if (!storage) {
    return "system";
  }
  try {
    const value = storage.getItem(THEME_STORAGE_KEY);
    return isThemeMode(value) ? value : "system";
  } catch {
    return "system";
  }
}

/** Applies a theme to the root element without persisting it. */
export function applyThemeMode(root: ThemeRoot, mode: ThemeMode): void {
  root.dataset.theme = mode;
}

/** Persists a theme best-effort; storage failures do not undo the applied mode. */
export function persistThemeMode(
  storage: Pick<ThemeStorage, "setItem"> | null,
  mode: ThemeMode
): void {
  try {
    storage?.setItem(THEME_STORAGE_KEY, mode);
  } catch {
    // Theme selection still applies to this tab when browser storage is unavailable.
  }
}

/** Reads and applies the initial theme before the app is rendered. */
export function initialiseTheme(
  root: ThemeRoot,
  storage: Pick<ThemeStorage, "getItem"> | null
): ThemeMode {
  const mode = readThemeMode(storage);
  applyThemeMode(root, mode);
  return mode;
}
