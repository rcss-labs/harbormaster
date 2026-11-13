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

// jsdom has no layout, so the wrapping contract is checked on the stylesheet: a long log message must wrap onto
// further lines instead of being cut off with an ellipsis.
describe('log line styling', () => {
  const css = readFileSync(join(__dirname, 'logs.module.css'), 'utf8')

  it('wraps long messages instead of cutting them off', () => {
    const message = ruleBody(css, '.msg')
    expect(message).toMatch(/white-space\s*:\s*pre-wrap/)
    expect(message).toMatch(/overflow-wrap\s*:\s*anywhere/)
    expect(message).not.toMatch(/text-overflow\s*:\s*ellipsis/)
  })

  it('does not force the whole row onto one line', () => {
    expect(ruleBody(css, '.ln')).not.toMatch(/white-space\s*:\s*nowrap/)
  })
})
