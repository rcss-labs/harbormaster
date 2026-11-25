import { describe, expect, it } from 'vitest'
import { refetchDelay } from './reviews'

describe('refetchDelay', () => {
  const start = 1_000_000
  it('keeps polling a waiting or running run, and only those', () => {
    expect(refetchDelay(true, start, start + 1_000, 5_000)).toBe(5_000)
    expect(refetchDelay(false, start, start + 1_000, 5_000)).toBe(false)
  })

  it('stops 30 minutes after the view opened', () => {
    expect(refetchDelay(true, start, start + 30 * 60_000 - 1, 5_000)).toBe(5_000)
    expect(refetchDelay(true, start, start + 30 * 60_000, 5_000)).toBe(false)
  })
})
