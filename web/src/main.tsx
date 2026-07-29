import { render } from "preact";

import { App } from "./App";
import { browserThemeStorage, initialiseTheme } from "./theme";

const appRoot = document.querySelector("#app");

if (!(appRoot instanceof HTMLElement)) {
  throw new Error("mdReview application root is missing");
}

const initialTheme = initialiseTheme(document.documentElement, browserThemeStorage());
render(<App initialTheme={initialTheme} />, appRoot);
