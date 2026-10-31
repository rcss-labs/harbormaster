import { defineConfig, devices } from '@playwright/test'

// HM_E2E_PORT lets parallel worktrees run e2e side by side; the default is unchanged.
const port = process.env.HM_E2E_PORT ?? '8481'
const baseURL = `http://127.0.0.1:${port}`

// `make e2e` points HM_E2E_DATABASE_URL at a freshly created schema; running
// `npm run e2e` directly falls back to the shared test database.
const databaseURL = process.env.HM_E2E_DATABASE_URL ?? process.env.HM_TEST_DATABASE_URL ?? ''

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: { baseURL, trace: 'retain-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: '../bin/harbormaster serve --fake-aws',
    env: {
      HM_LISTEN: `127.0.0.1:${port}`,
      HM_COOKIE_SECURE: 'false',
      HM_DATABASE_URL: databaseURL,
    },
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 60_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
})
