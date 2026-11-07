import { describe, expect, it } from 'vitest'
import { retentionText } from './format'

describe('retentionText', () => {
  it('names whole-day retentions in days, with a year for 8760h', () => {
    expect(retentionText('2160h')).toBe('90 days')
    expect(retentionText('168h')).toBe('7 days')
    expect(retentionText('720h')).toBe('30 days')
    expect(retentionText('8760h')).toBe('1 year')
  })

  it('rounds other hour values to whole days', () => {
    expect(retentionText('100h')).toBe('4 days')
    expect(retentionText('0h')).toBe('0 days')
  })

  it('an empty retention means kept forever', () => {
    expect(retentionText('')).toBe('forever')
  })
})
