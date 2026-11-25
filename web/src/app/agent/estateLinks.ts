/**
 * Which paths a model's answer may link to. Model text can come from untrusted logs, so a link is made only to a page that
 * shows estate data. Settings, the user's own pages, sign-in, OAuth, admin pages and the task terminal are text.
 */

/** First path segments of the estate pages in `app/router.tsx` (the overview is the path "/" itself). A test keeps this in step with the router. */
export const ESTATE_PAGE_PREFIXES: readonly string[] = ['clusters', 'services', 'tasks', 'networks', 'storage', 'analytics', 'security', 'logs', 'config']

/** A path on this site: one leading "/" that is not "//" or "/\\", with no whitespace. */
const SAME_ORIGIN_PATH = /^\/(?![/\\])[^\s\\]*$/

/** C0 controls, DEL and the C1 controls (U+0080 to U+009F, such as the next-line character U+0085). */
// eslint-disable-next-line no-control-regex -- matching control characters is the point
const CONTROL_CHARACTER = /[\u0000-\u001f\u007f-\u009f]/

const MAX_DECODE_ROUNDS = 5

/** Decodes percent-encoding until nothing changes, so a doubly encoded name cannot hide; null for a malformed or never-ending one. */
function fullyDecoded(value: string): string | null {
  let current = value
  for (let round = 0; round < MAX_DECODE_ROUNDS; round++) {
    let next: string
    try {
      next = decodeURIComponent(current)
    } catch {
      return null
    }
    if (next === current) return current
    current = next
  }
  return null
}

/** The path with "." and ".." segments resolved as a browser would; null when it climbs above the root or has an empty segment inside. */
function resolvedSegments(path: string): string[] | null {
  const parts = path.split('/').slice(1)
  const out: string[] = []
  for (const [index, part] of parts.entries()) {
    if (part === '.') continue
    if (part === '..') {
      if (out.pop() === undefined) return null
      continue
    }
    if (part === '' && index !== parts.length - 1) return null
    out.push(part)
  }
  if (out[out.length - 1] === '') out.pop()
  return out
}

/** True for a path (with optional query and fragment) that is an estate page of this app. */
export function isEstatePath(target: string): boolean {
  if (!SAME_ORIGIN_PATH.test(target) || CONTROL_CHARACTER.test(target)) return false
  const decoded = fullyDecoded(target)
  if (decoded === null || CONTROL_CHARACTER.test(decoded) || decoded.includes('\\')) return false
  const path = decoded.slice(0, decoded.search(/[?#]|$/))
  if (!path.startsWith('/') || path.startsWith('//')) return false
  const segments = resolvedSegments(path)
  if (segments === null) return false
  const names = segments.map((segment) => segment.toLowerCase())
  if (names.length === 0) return true
  if (names[names.length - 1] === 'terminal') return false
  return ESTATE_PAGE_PREFIXES.includes(names[0])
}
