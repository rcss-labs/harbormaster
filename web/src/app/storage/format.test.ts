import { describe, expect, it } from 'vitest'
import { formatMiBps, formatSize } from './format'

describe('formatSize', () => {
  it.each([
    [21474836, '20 MiB'],
    [41231686041, '38.4 GiB'],
    [227204155801, '211.6 GiB'],
    [0, '0 MiB'],
  ])('formats %i bytes as %s', (bytes, text) => {
    expect(formatSize(bytes)).toBe(text)
  })
})

describe('formatMiBps', () => {
  it('shows whole numbers from 10 MiB/s up', () => {
    expect(formatMiBps(29.4)).toBe('29 MiB/s')
  })

  it('shows one decimal below 10 MiB/s', () => {
    expect(formatMiBps(5.37)).toBe('5.4 MiB/s')
  })
})
