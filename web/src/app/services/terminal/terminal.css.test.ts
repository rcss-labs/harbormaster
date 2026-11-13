import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/** The declaration block of the first rule whose selector is exactly `selector`. */
function ruleBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = new RegExp(`(?:^|[}/])\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(css)
  if (!match) throw new Error(`no rule for ${selector}`)
  return match[1]
}

// xterm's fit addon divides the host's computed width by the cell width and ignores the host's own padding and border,
// so a padded host gets more columns than are visible: long lines are cut off at the host's edge instead of wrapping,
// and the remote shell is told the wider size too. The inset therefore has to live on .xterm, which fit subtracts.
describe('terminal host styling', () => {
  const css = readFileSync(join(__dirname, 'terminal.module.css'), 'utf8')

  it('keeps padding and border off the element xterm fits to, on every viewport', () => {
    const host = ruleBody(css, '.term')
    expect(host).not.toMatch(/(^|[\s;])padding\s*:(?!\s*0\s*(;|$))/)
    expect(host).not.toMatch(/(^|[\s;])border\s*:(?!\s*(0|none)\s*(;|$))/)
  })

  it('insets the terminal itself, where the fit addon accounts for it', () => {
    expect(ruleBody(css, '.term :global(.xterm)')).toMatch(/padding\s*:/)
  })
})
