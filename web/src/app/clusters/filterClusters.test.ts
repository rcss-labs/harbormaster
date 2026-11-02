import { describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { filterClusters } from './filterClusters'

const names = (f: Parameters<typeof filterClusters>[1]) => filterClusters(CLUSTERS.items, f).map((c) => c.name)

describe('filterClusters', () => {
  it('keeps clusters whose status is selected', () => {
    expect(names({ status: ['healthy'], q: '' })).toEqual(['ci-tools', 'edge-proxy', 'staging-web'])
  })
  it('matches the name case-insensitively as a substring', () => {
    expect(names({ status: [], q: 'EU' })).toEqual(['eu-apps'])
  })
  it('combines status and name', () => {
    expect(names({ status: ['degraded', 'needs_attention'], q: 'o' })).toEqual(['background-jobs'])
  })
  it('treats a blank name as no filter', () => {
    expect(names({ status: [], q: '  ' })).toHaveLength(6)
  })
})
