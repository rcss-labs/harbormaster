import { defineConfig, devices } from '@playwright/test'

// HM_E2E_PORT lets parallel worktrees run e2e side by side; the default is unchanged.
const port = process.env.HM_E2E_PORT ?? '8481'
const baseURL = `http://127.0.0.1:${port}`
// The degraded project runs against a second server (HM_FAKE_FAIL set) one port up, so the healthy server's pollers
// never overwrite its failing tiers.
// HM_E2E_DEGRADED_PORT overrides it where the next port belongs to another worktree's lane.
const degradedPort = process.env.HM_E2E_DEGRADED_PORT ?? String(Number(port) + 1)
const degradedBaseURL = `http://127.0.0.1:${degradedPort}`

// `make e2e` creates a fresh hm_e2e schema and passes its URL in HM_E2E_DATABASE_URL.
// Never fall back to the raw test database URL: the server would seed demo data into its public schema.
const databaseURL = process.env.HM_E2E_DATABASE_URL
if (!databaseURL) {
  throw new Error('HM_E2E_DATABASE_URL is not set; run the e2e suite with `make e2e` (it prepares the dedicated hm_e2e schema)')
}

// The degraded server needs its own schema (a second server on the healthy one would poll over it). It lives next to
// the healthy one: same database, schema `<name>_degraded`, recreated by tools/e2eschema before the server starts.
const healthyURL = new URL(databaseURL)
const healthySchema = /search_path[= ]+([a-z_][a-z0-9_]*)/.exec(decodeURIComponent(healthyURL.searchParams.get('options') ?? ''))?.[1] ?? 'hm_e2e'
const degradedSchema = `${healthySchema}_degraded`
const baseDatabaseURL = new URL(databaseURL)
baseDatabaseURL.searchParams.delete('options')
// The server's URL parser wants %20 for the space, not the "+" that searchParams would write.
const degradedDatabaseURL = new URL(baseDatabaseURL)
degradedDatabaseURL.search += `${degradedDatabaseURL.search ? '&' : '?'}options=${encodeURIComponent(`-c search_path=${degradedSchema}`)}`

const serverEnv = {
  HM_COOKIE_SECURE: 'false',
  HM_SESSION_KEY: 'e2e-session-key', // plan 07: SAML request state and SP key encryption
  // Short observe tiers keep the observe specs from waiting a minute for their first figures.
  HM_POLL_SCALING: '5s',
  HM_POLL_TARGET_HEALTH: '5s',
  HM_POLL_NETWORK: '5s', // plan 05
  HM_POLL_STORAGE: '5s', // plan 05
  HM_METRICS_TOKEN: 'e2e-metrics-token', // plan 06
  HM_SCAN_INTERVAL: '3s', // image security: the scan loop picks a requested scan up within seconds
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
    // v4: services, shell and services-live read the unmutated seed, so they run here. attention and task-drawer mutate the server,
    // so they fall through to chromium-flows (everything not matched below). freshness and deploy-partial run in chromium-degraded.
    { name: 'chromium-observe', testMatch: /((observe|networks|storage|security|overview|clusters|cluster|tasks|layout|services|shell|services-live)\.spec\.ts|(^|\/)me\.spec\.ts$|clusters-filters\.spec\.ts$|tag-filter\.spec\.ts$)/, // plan 05/06: plan 06 (task 17) added overview, clusters, cluster, tasks; task 19 added me (anchored): read-only networks/storage specs read the unmutated seed
      dependencies: ['chromium'], retries: 1, use: { ...devices['Desktop Chrome'] } },
    { name: 'chromium-flows', testIgnore: /(\.mobile(-flows)?\.spec\.ts$|(smoke|observe|networks|storage|security|overview|clusters|cluster|tasks|layout|services|shell|services-live|freshness|deploy-partial)\.spec\.ts|(^|\/)me\.spec\.ts$|clusters-filters\.spec\.ts$|tag-filter\.spec\.ts$)/, // plan 05/06, plan 06 task 17/19; phone specs have their own project
      dependencies: ['chromium-mobile'], use: { ...devices['Desktop Chrome'] } },
    // Phone specs run at 390 x 844 and are picked by filename. `*.mobile.spec.ts` only read, so they see the unmutated seed like
    // chromium-observe, before chromium-flows changes the estate. `*.mobile-flows.spec.ts` change data, so they follow chromium-flows.
    { name: 'chromium-mobile', testMatch: /\.mobile\.spec\.ts$/, dependencies: ['chromium-observe'],
      use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 } },
    { name: 'chromium-mobile-flows', testMatch: /\.mobile-flows\.spec\.ts$/, dependencies: ['chromium-flows'],
      use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 } },
    // v4 plan Decision 17: scheduled actions and the scaling tier fail on a second server, so the specs see stale data and a
    // partial Deploy tab. It has its own database schema, so the specs sign in themselves instead of using e2e/.auth.
    { name: 'chromium-degraded', testMatch: /(freshness|deploy-partial)\.spec\.ts/,
      dependencies: ['chromium-mobile-flows'], use: { ...devices['Desktop Chrome'], baseURL: degradedBaseURL } },
  ],
  webServer: [
    {
      command: '../bin/harbormaster serve --fake-aws',
      env: {
        ...serverEnv,
        HM_LISTEN: `127.0.0.1:${port}`,
        // The fake IdP and SP metadata use this origin; it must match the port Playwright serves on.
        HM_BASE_URL: baseURL,
        HM_DATABASE_URL: databaseURL,
      },
      url: `${baseURL}/healthz`,
      reuseExistingServer: false,
      timeout: 60_000,
      gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
    },
    {
      command: 'go run ./tools/e2eschema >/dev/null && exec ./bin/harbormaster serve --fake-aws',
      cwd: '..',
      env: {
        ...serverEnv,
        HM_FAKE_FAIL: 'scheduled_actions,scaling_tier',
        HM_LISTEN: `127.0.0.1:${degradedPort}`,
        HM_BASE_URL: degradedBaseURL,
        HM_DATABASE_URL: degradedDatabaseURL.toString(),
        // tools/e2eschema recreates the degraded schema from the base URL, then prints the URL (discarded above).
        HM_TEST_DATABASE_URL: baseDatabaseURL.toString(),
        HM_E2E_SCHEMA: degradedSchema,
      },
      url: `${degradedBaseURL}/healthz`,
      reuseExistingServer: false,
      timeout: 60_000,
      gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
    },
  ],
})
