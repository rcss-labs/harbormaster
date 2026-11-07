import { defineConfig, devices } from '@playwright/test'

// Checks the real UI against the floci estate (see tools/floci).
// `make floci-check` builds the binary, prepares the database and runs this config; the webServer below starts
// tools/floci/serve.sh, which sources the isolated AWS environment and refuses any non-local endpoint.
const port = process.env.HM_FLOCI_PORT ?? '8482'
const baseURL = `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './e2e-floci',
  testMatch: /(floci\.setup|routes\.spec|focused\.spec)\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    { name: 'setup', testMatch: /floci\.setup\.ts/, use: { ...devices['Desktop Chrome'] } },
    {
      name: 'routes',
      testMatch: /routes\.spec\.ts/,
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'], storageState: '.auth-floci/admin.json' },
    },
    {
      name: 'focused',
      testMatch: /focused\.spec\.ts/,
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'], storageState: '.auth-floci/admin.json' },
    },
  ],
  webServer: {
    command: '../tools/floci/serve.sh',
    url: `${baseURL}/healthz`,
    // Set HM_FLOCI_REUSE=1 to run against a `make floci-serve` that is already up.
    reuseExistingServer: !!process.env.HM_FLOCI_REUSE,
    timeout: 60_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
    env: { HM_FLOCI_PORT: port },
  },
})
