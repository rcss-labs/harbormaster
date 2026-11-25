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

const MAX_DECODE_ROUNDS = 3

/**
 * What a path segment can turn into once percent-encoding is undone (at most MAX_DECODE_ROUNDS times), or null when that is
 * not a harmless name: malformed or still encoded after the last round, or holding a separator, query or hash marker,
 * backslash, control character or a dot segment.
 */
function decodedSegment(segment: string): string | null {
  let current = segment
  for (let round = 0; current.includes('%'); round++) {
    if (round === MAX_DECODE_ROUNDS) return null
    try {
      current = decodeURIComponent(current)
    } catch {
      return null
    }
  }
  if (/[/\\?#]/.test(current) || CONTROL_CHARACTER.test(current) || current === '.' || current === '..') return null
  return current
}

/**
 * True for a path (with optional query and fragment) that is an estate page of this app. The check reads the raw text: the
 * path is cut at the first raw "?" or "#" and split on raw "/", which is what a browser and the router do with it. Anything
 * odd (a dot segment, an encoded separator, an empty segment, a terminal page) is refused, never resolved. The first segment
 * is exactly an estate page's name. The query and fragment are not decoded.
 */
export function isEstatePath(target: string): boolean {
  if (!SAME_ORIGIN_PATH.test(target) || CONTROL_CHARACTER.test(target)) return false
  const path = target.slice(0, target.search(/[?#]|$/))
  if (path === '/') return true
  const [first, ...rest] = path.split('/').slice(1)
  if (!ESTATE_PAGE_PREFIXES.includes(first)) return false
  return rest.every((segment, index) => {
    if (segment === '') return index === rest.length - 1
    const decoded = decodedSegment(segment)
    return decoded !== null && decoded.toLowerCase() !== 'terminal'
  })
}
