import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/browser",
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "retain-on-failure",
    viewport: {
      width: 1280,
      height: 800
    }
  },
  projects: [
    {
      name: "chromium",
      use: {
        browserName: "chromium"
      }
    },
    {
      name: "firefox",
      use: {
        browserName: "firefox"
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
