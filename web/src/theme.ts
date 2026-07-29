export const THEME_STORAGE_KEY = "mdreview.theme";

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

export function isThemeMode(value: unknown): value is ThemeMode {
  return value === "light" || value === "dark" || value === "system";
}

export function browserThemeStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export function readThemeMode(storage: Pick<ThemeStorage, "getItem"> | null): ThemeMode {
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

export function applyThemeMode(root: ThemeRoot, mode: ThemeMode): void {
  root.dataset.theme = mode;
}

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

export function initialiseTheme(
  root: ThemeRoot,
  storage: Pick<ThemeStorage, "getItem"> | null
): ThemeMode {
  const mode = readThemeMode(storage);
  applyThemeMode(root, mode);
  return mode;
}
