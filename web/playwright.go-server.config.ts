import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/go-server",
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: "line",
  globalSetup: "./tests/go-server/global-setup.mjs",
  use: {
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
  ]
});
