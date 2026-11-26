import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { ESTATE_PAGE_PREFIXES, isEstatePath } from './estateLinks'

const ALLOWED = new Set<string>(ESTATE_PAGE_PREFIXES)

/** A small deterministic generator, so a failure repeats. */
function random(seed: number) {
  let state = seed
  return () => {
    state = (state * 1664525 + 1013904223) % 4294967296
    return state / 4294967296
  }
}

describe('isEstatePath (property)', () => {
  it('whatever is accepted lands on an estate page of the browser, and never on a terminal', () => {
    const next = random(20261125)
    const pieces = [
      '/', '/', '/', '..', '.', '%2e', '%2E', '%2e%2e', '%2f', '%2F', '%5c', '%3f', '%3F', '%23', '%25', '%253F', '%252e', '%00', '%C2%85', '%',
      '?', '#', 'a', 'b', 'x1', 'terminal', 'TERMINAL', 'Terminal', '%74erminal', '=', '&', 'q=1', ' ', '\\', '\u0085',
      'services', 'clusters', 'tasks', 'networks', 'storage', 'analytics', 'security', 'logs', 'config',
      'settings', 'me', 'oauth', 'login', 'admin', 'password', 'setup', 'more', 'api',
    ]
    let accepted = 0
    for (let i = 0; i < 5000; i++) {
      let target = next() < 0.8 ? '/' + pieces[Math.floor(next() * pieces.length)] : ''
      for (let n = Math.floor(next() * 8); n > 0; n--) target += pieces[Math.floor(next() * pieces.length)]
      if (!isEstatePath(target)) continue
      accepted++
      const path = new URL(target, 'https://x').pathname
      const segments = path.split('/').slice(1)
      expect(path === '/' || ALLOWED.has(segments[0]), `${target} -> ${path}`).toBe(true)
      for (const segment of segments) {
        let decoded = segment
        try {
          for (let round = 0; round < 5; round++) decoded = decodeURIComponent(decoded)
        } catch {
          // an undecodable segment is not a name for a page
        }
        expect(decoded.toLowerCase(), `${target} -> ${path}`).not.toBe('terminal')
      }
    }
    expect(accepted).toBeGreaterThan(50)
  })
})

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
    '/logs?q=100%25',
    '/services/abc%20def',
    '/services/abc/overview?range=1h#cpu',
    '/services/',
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
    '/tasks/terminal',
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
    ['a dot segment that stays inside', '/services/./abc'],
    ['a dot segment that returns', '/services/abc/../def'],
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
    ['encoded ? then dot segments (the router cuts at the ?)', '/services/x%3F/../../settings/users'],
    ['encoded # then dot segments', '/services/x%23/../../me/access'],
    ['encoded ? before a terminal page', '/tasks/a%3F/../y/terminal'],
    ['encoded slash then dot segments', '/clusters/a%2Fb/../../settings/users'],
    ['encoded slash then dot segments to OAuth', '/clusters/a%2Fb/../../oauth/authorize?client_id=evil'],
    ['encoded slash with encoded dot segments', '/tasks/a%2Fb/%2e%2e/%2e%2e/settings'],
    ['doubly encoded ?', '/services/x%253F/../../settings'],
    ['three times encoded ?', '/services/x%25253F/y'],
    ['encoded more than three times', '/services/x%2525252525/y'],
    ['encoded terminal after the first segment', '/tasks/abc/%54ERMINAL'],
    ['upper-case first segment', '/SERVICES/abc'],
    ['encoded first segment', '/%73ervices/abc'],
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
