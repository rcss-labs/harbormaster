import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = readFileSync(join(__dirname, 'tokens.css'), 'utf8')

/** Body of the first rule whose selector (text before `{`) contains `selector`. */
function ruleBody(selector: string): string {
  const start = css.indexOf(selector)
  expect(start, `${selector} rule exists`).toBeGreaterThanOrEqual(0)
  const open = css.indexOf('{', start)
  return css.slice(open + 1, css.indexOf('}', open))
}

const REQUIRED = ['ok', 'warn', 'bad', 'info', 'exp', 'mute', 's1', 's2', 's3', 's4', 's5', 's6']

describe('tokens.css', () => {
  it.each([
    ['dark', ':root[data-theme="dark"] {'],
    ['light', ':root[data-theme="light"] {'],
  ])('%s defines --ok --warn --bad --info --exp --mute --s1..--s6', (_name, selector) => {
    const body = ruleBody(selector)
    for (const token of REQUIRED) expect(body).toMatch(new RegExp(`--${token}:`))
  })

  it('defines --row and --pad, with a comfortable density override', () => {
    expect(css).toMatch(/--row: 36px/)
    expect(css).toMatch(/--pad: 12px/)
    expect(css).toMatch(/:root\[data-density="comfortable"\]\s*\{\s*--row: 44px;\s*--pad: 16px;/)
  })

  it('keeps the OS dark preference fallback', () => {
    expect(css).toMatch(/@media \(prefers-color-scheme: dark\)\s*\{\s*:root:not\(\[data-theme="light"\]\)/)
  })

  it('legacy names are aliases of the new ones', () => {
    const aliases: Record<string, string> = {
      success: 'ok',
      warning: 'warn',
      danger: 'bad',
      expected: 'exp',
      'series-1': 's1',
      'series-6': 's6',
      dim: 'muted',
      'hover-strong': 'active',
      'accent-soft-border': 'accent-line',
      radius: 'r',
      'radius-sm': 'r-s',
      'shadow-pop': 'pop',
      overlay: 'scrim',
      'term-bg': 'term',
      'healthy-cell': 'cell-ok',
    }
    for (const [legacy, current] of Object.entries(aliases)) {
      expect(css, `--${legacy}`).toContain(`--${legacy}: var(--${current});`)
    }
  })

  it('derives the tag pairs from the state colours', () => {
    expect(css).toMatch(/--tag-red-text: var\(--bad\)/)
    expect(css).toMatch(/--tag-green-text: var\(--ok\)/)
    expect(css).toMatch(/--tag-amber-text: var\(--warn\)/)
  })
})
