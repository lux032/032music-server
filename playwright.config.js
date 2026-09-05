const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests/e2e',
  timeout: 30_000,
  expect: { timeout: 8_000 },
  workers: 1,
  reporter: [['list'], ['html', { outputFolder: 'playwright-report', open: 'never' }]],
  use: {
    baseURL: 'http://127.0.0.1:45439',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  },
  globalSetup: './scripts/e2e-global-setup.mjs',
  globalTeardown: './scripts/e2e-global-teardown.mjs',
  projects: [
    { name: 'desktop-chromium', use: { browserName: 'chromium', viewport: { width: 1280, height: 800 } } },
    { name: 'mobile-375', use: { browserName: 'chromium', viewport: { width: 375, height: 760 }, hasTouch: true } },
    { name: 'boundary-820', use: { browserName: 'chromium', viewport: { width: 820, height: 760 }, hasTouch: true } }
  ]
});
