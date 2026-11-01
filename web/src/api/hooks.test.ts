import { describe, expect, it } from 'vitest'
import { queryString } from './hooks'

describe('queryString', () => {
  it('repeats the key once per array element', () => {
    expect(queryString({ tag: [2, 3], account: 'a' })).toBe('?account=a&tag=2&tag=3')
  })

  it('omits empty arrays', () => {
    expect(queryString({ tag: [] })).toBe('')
  })

  it('omits undefined and empty values', () => {
    expect(queryString({ tag: undefined, q: '' })).toBe('')
  })
})
