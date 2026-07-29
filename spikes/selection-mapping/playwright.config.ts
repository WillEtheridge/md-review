import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/browser",
  fullyParallel: true,
  retries: 0,
  reporter: [["list"], ["html", { open: "never", outputFolder: "artifacts/report" }]],
  outputDir: "artifacts/test-results",
  use: {
    baseURL: "http://127.0.0.1:41731",
    trace: "retain-on-failure"
  },
  webServer: {
    command: "npm run dev",
    url: "http://127.0.0.1:41731",
    reuseExistingServer: false,
    timeout: 120_000
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] }
    },
    {
      name: "firefox",
      use: { ...devices["Desktop Firefox"] }
    }
  ]
});
