import { asUser, expect, test } from './fixtures/test'

// Copied from CSPValue in internal/api/security_headers.go.
const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"

test.describe('security', () => {
  test.use(asUser('pavel'))

  test('every response carries security headers', async ({ request }) => {
    for (const url of ['/', '/api/nope', '/healthz']) {
      const res = await request.get(url)
      const headers = res.headers()
      expect(headers['content-security-policy'], url).toBe(CSP)
      expect(headers['strict-transport-security'], url).toBeTruthy()
      expect(headers['x-frame-options'], url).toBe('DENY')
      expect(headers['x-content-type-options'], url).toBe('nosniff')
    }
  })

  test('metrics needs the bearer token', async ({ request }) => {
    expect((await request.get('/metrics')).status()).toBe(401)
    const ok = await request.get('/metrics', { headers: { Authorization: 'Bearer e2e-metrics-token' } })
    expect(ok.status()).toBe(200)
    expect(await ok.text()).toMatch(/^harbormaster_poller_leader [01]$/m)
  })

  test('no CSP violations on the overview', async ({ page }) => {
    // The automatic cspViolations fixture fails the test after it ends if any violation was recorded.
    await page.goto('/')
    await page.waitForLoadState('networkidle')
  })

  test('favicon is served as SVG', async ({ request }) => {
    const res = await request.get('/brand/favicon.svg')
    expect(res.status()).toBe(200)
    expect(res.headers()['content-type']).toContain('image/svg+xml')
  })
})
