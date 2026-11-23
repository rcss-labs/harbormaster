import { defineConfig, devices } from '@playwright/test'

// Checks the access token UI and the OAuth consent route against the floci estate (see tools/floci).
// `make floci-mcp-check` builds the binary, prepares the database and runs this config. The webServer starts
// tools/floci/serve-tokens.sh (HM_API_TOKENS and HM_OAUTH on) on its own port, 8491 by default, so it never collides
// with the plain floci suite on 8490. The setup project is the floci one: it runs connect.sh and keeps the admin session.
// Both servers share one Postgres, so do not run this next to make floci-serve or make floci-check.
const port = process.env.HM_FLOCI_PORT ?? '8491'
// connect.sh (run by the setup project) and the server read HM_FLOCI_PORT from the environment.
process.env.HM_FLOCI_PORT = port
const baseURL = `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './e2e-floci',
  testMatch: /(floci\.setup|tokens\.spec)\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    { name: 'setup', testMatch: /floci\.setup\.ts/, use: { ...devices['Desktop Chrome'] } },
    {
      name: 'tokens',
      testMatch: /tokens\.spec\.ts/,
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'], storageState: '.auth-floci/admin.json' },
    },
  ],
  webServer: {
    command: '../tools/floci/serve-tokens.sh',
    url: `${baseURL}/healthz`,
    // Set HM_FLOCI_REUSE=1 to run against a serve-tokens.sh that is already up.
    reuseExistingServer: !!process.env.HM_FLOCI_REUSE,
    timeout: 60_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
    env: { HM_FLOCI_PORT: port },
  },
})
