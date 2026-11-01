import { describe, expect, it } from 'vitest'
import { level } from './level'

// Same table as internal/logs TestLevel_Table so the client and server heuristics stay twins.
describe('level', () => {
  const cases: [string, 'error' | 'warn' | ''][] = [
    ['level=error x', 'error'],
    ['{"level":"warn"}', 'warn'],
    ['ERROR: boom', 'error'],
    ['Errors: 0', ''],
    ['[WARN] x', 'warn'],
    ['warned', ''],
    ['level=warning slow', 'warn'],
    ['FATAL: out of memory', 'error'],
    ['GET /health 200 2ms', ''],
    ['WARN slow then ERROR boom', 'error'],
  ]
  it.each(cases)('classifies %j as %j', (message, expected) => {
    expect(level(message)).toBe(expected)
  })
})
