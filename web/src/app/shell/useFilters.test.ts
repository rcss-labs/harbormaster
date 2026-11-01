import { describe, expect, it } from 'vitest'
import { ME_ADMIN, ME_OMER } from '../../test/fixtures'
import { visibleTagIds } from './useFilters'

describe('visibleTagIds', () => {
  it('gives admins every tag', () => {
    expect(visibleTagIds(ME_ADMIN)).toEqual([1, 2, 3])
  })

  it('keeps only tags with the View bit', () => {
    // Tag 2 is listed but its permission lacks View (bit 1); tag 3 has no permission at all.
    const me = { ...ME_OMER, tags: ME_ADMIN.tags, perms: { '1': 7, '2': 2 } }
    expect(visibleTagIds(me)).toEqual([1])
  })
})
