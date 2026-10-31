import { defineConfig, devices } from '@playwright/test'

const baseURL = 'http://127.0.0.1:8481'

// `make e2e` creates a fresh hm_e2e schema and passes its URL in HM_E2E_DATABASE_URL.
// Never fall back to the raw test database URL: the server would seed demo data into its public schema.
const databaseURL = process.env.HM_E2E_DATABASE_URL
if (!databaseURL) {
  throw new Error('HM_E2E_DATABASE_URL is not set; run the e2e suite with `make e2e` (it prepares the dedicated hm_e2e schema)')
}

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
      HM_LISTEN: '127.0.0.1:8481',
      HM_COOKIE_SECURE: 'false',
      HM_DATABASE_URL: databaseURL,
    },
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 60_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
})
