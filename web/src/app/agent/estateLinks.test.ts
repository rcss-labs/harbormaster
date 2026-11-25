import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { ESTATE_PAGE_PREFIXES, isEstatePath } from './estateLinks'

describe('isEstatePath', () => {
  it.each([
    '/',
    '/?x=1',
    '/clusters',
    '/clusters/core-services',
    '/services/abc?x=1',
    '/services/abc/overview#top',
    '/tasks',
    '/tasks/abc123',
    '/networks/123456789012/eu-west-1/vpc-0abc',
    '/storage',
    '/analytics/availability',
    '/security',
    '/logs?q=error',
    '/config',
    '/services/./abc',
    '/services/abc/../def',
    '/SERVICES/abc',
  ])('links to %s', (target) => {
    expect(isEstatePath(target)).toBe(true)
  })

  it.each([
    '/settings',
    '/settings/users',
    '/me/access',
    '/oauth/authorize?client_id=x',
    '/login',
    '/setup',
    '/password',
    '/admin/users',
    '/more',
    '/apis/x',
    '/servicesx',
    '/api/auth/logout',
    '/tasks/abc123/terminal',
    '/tasks/abc123/terminal/',
    '/tasks/abc123/TERMINAL',
    '/tasks/abc123/terminal?x=1',
    '/tasks/abc123/terminal#x',
  ])('shows %s as text', (target) => {
    expect(isEstatePath(target)).toBe(false)
  })

  it.each([
    ['percent-encoded name', '/%73ettings/general'],
    ['percent-encoded dot segments', '/services/%2e%2e/settings'],
    ['upper-case percent-encoded dot segments', '/services/%2E%2E/settings'],
    ['encoded slash before dots', '/services/..%2fsettings'],
    ['encoded slash and dots', '/services/%2e%2e%2fsettings'],
    ['double encoding', '/services/%252e%252e/settings'],
    ['dot segments', '/services/../settings'],
    ['dot segments past the root', '/services/../../../settings'],
    ['single dot then up', '/clusters/./../me/access'],
    ['dot segments to terminal', '/tasks/x/../y/terminal'],
    ['encoded terminal', '/tasks/abc/%74erminal'],
    ['encoded backslash', '/services/..%5csettings'],
    ['backslash', '/services/..\\settings'],
    ['malformed percent-encoding', '/services/%zz'],
    ['truncated percent-encoding', '/services/abc%'],
    ['an empty segment', '/services//settings'],
    ['U+0085 in the path', '/services/\u0085/x'],
    ['encoded U+0085', '/services/%C2%85settings'],
    ['encoded C1 control', '/services/%C2%9Fx'],
    ['U+0085 in the query', '/services/abc?x=\u0085'],
    ['encoded newline', '/services/abc%0Asettings'],
    ['encoded tab', '/services/%09/x'],
    ['protocol-relative', '//example.com/services'],
    ['an external URL', 'https://example.com/services'],
    ['no leading slash', 'services/abc'],
  ])('shows %s as text', (_name, target) => {
    expect(isEstatePath(target)).toBe(false)
  })

  it('the allow-list is the estate pages of the router and nothing else', () => {
    const router = readFileSync(join(process.cwd(), 'src/app/router.tsx'), 'utf8')
    const routed = [...router.matchAll(/<Route path="([a-z]+)[/":]/g)].map((match) => match[1])
    for (const prefix of ESTATE_PAGE_PREFIXES) expect(routed, `${prefix} is not a route`).toContain(prefix)
    for (const notEstate of ['settings', 'me', 'oauth', 'login', 'setup', 'password', 'admin', 'more', 'api']) {
      expect(ESTATE_PAGE_PREFIXES, notEstate).not.toContain(notEstate)
    }
    expect([...ESTATE_PAGE_PREFIXES].sort()).toEqual(['analytics', 'clusters', 'config', 'logs', 'networks', 'security', 'services', 'storage', 'tasks'])
  })
})
