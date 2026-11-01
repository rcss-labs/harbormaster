import { defineConfig, devices } from '@playwright/test'

// HM_E2E_PORT lets parallel worktrees run e2e side by side; the default is unchanged.
const port = process.env.HM_E2E_PORT ?? '8481'
const baseURL = `http://127.0.0.1:${port}`

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
  use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  // The smoke suite reads the untouched seeded estate (for example a rollout that is still in progress), then the
  // observe suite reads it before any flow changes it (it expects image-resizer failing and payments-api's log history),
  // so every mutating spec runs in a last project that waits for both. The read-only networks and storage specs (plan 05)
  // run in the observe project too, so they also read the unmutated seed.
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.ts/, use: { ...devices['Desktop Chrome'] } }, // plan 06: signs in each role, writes e2e/.auth/*.json
    { name: 'chromium', testMatch: /smoke\.spec\.ts/, dependencies: ['setup'], use: { ...devices['Desktop Chrome'] } }, // plan 06: dependencies
    // retries: 1 because a failed dependency skips every flow spec; the observe specs only read, so a retry is safe.
    { name: 'chromium-observe', testMatch: /((observe|networks|storage|security|overview|clusters|cluster|tasks)\.spec\.ts|(^|\/)me\.spec\.ts$)/, // plan 05/06: plan 06 (task 17) added overview, clusters, cluster, tasks; task 19 added me (anchored): read-only networks/storage specs read the unmutated seed
      dependencies: ['chromium'], retries: 1, use: { ...devices['Desktop Chrome'] } },
    { name: 'chromium-flows', testIgnore: /((smoke|observe|networks|storage|security|overview|clusters|cluster|tasks)\.spec\.ts|(^|\/)me\.spec\.ts$)/, // plan 05/06, plan 06 task 17/19
      dependencies: ['chromium-observe'], use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: '../bin/harbormaster serve --fake-aws',
    env: {
      HM_LISTEN: `127.0.0.1:${port}`,
      HM_COOKIE_SECURE: 'false',
      HM_DATABASE_URL: databaseURL,
      // Short observe tiers keep the observe specs from waiting a minute for their first figures.
      HM_POLL_SCALING: '5s',
      HM_POLL_TARGET_HEALTH: '5s',
      HM_POLL_NETWORK: '5s', // plan 05
      HM_POLL_STORAGE: '5s', // plan 05
      HM_METRICS_TOKEN: 'e2e-metrics-token', // plan 06
    },
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 60_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
})
