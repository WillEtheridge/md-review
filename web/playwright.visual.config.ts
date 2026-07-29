import { defineConfig } from "@playwright/test";

const commonUse = {
  baseURL: "http://127.0.0.1:4173",
  browserName: "chromium" as const,
  deviceScaleFactor: 1,
  locale: "en-GB",
  reducedMotion: "reduce" as const,
  serviceWorkers: "block" as const,
  timezoneId: "UTC",
  trace: "retain-on-failure" as const
};

export default defineConfig({
  testDir: "./tests/visual",
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: "line",
  updateSnapshots: "none",
  expect: {
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      scale: "css"
    }
  },
  projects: [
    {
      name: "chromium-1440",
      testMatch: "**/m4-1440.visual.spec.ts",
      use: {
        ...commonUse,
        viewport: {
          width: 1440,
          height: 1000
        }
      }
    },
    {
      name: "chromium-1280",
      testMatch: "**/m4-1280.visual.spec.ts",
      use: {
        ...commonUse,
        viewport: {
          width: 1280,
          height: 800
        }
      }
    },
    {
      name: "chromium-900",
      testMatch: "**/m4-900.visual.spec.ts",
      use: {
        ...commonUse,
        viewport: {
          width: 900,
          height: 700
        }
      }
    }
  ],
  webServer: {
    command: "npm run preview:test",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
    timeout: 30_000
  }
});
