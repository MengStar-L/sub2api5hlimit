import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  testMatch: 'production.spec.ts',
  globalSetup: './tests/e2e/production.global-setup.ts',
  outputDir: './output/playwright/production-results',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [['list'], ['html', { outputFolder: './output/playwright/production-report', open: 'never' }]],
  use: {
    ...devices['Desktop Chrome'],
    viewport: { width: 1440, height: 960 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'production-go-chromium' }],
})
