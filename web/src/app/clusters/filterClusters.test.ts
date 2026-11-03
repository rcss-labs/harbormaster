import { describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { TAG_DEV, TAG_PROD, TAG_STAGING } from '../../test/fixtures'
import { clusterCounts, clusterSeverity, filterClusters, resolveTagFilter, type ClusterPageFilters } from './filterClusters'

const QA = { id: 9, name: 'QA' }
const VISIBLE = [TAG_DEV, TAG_STAGING, TAG_PROD, QA]

const base: ClusterPageFilters = { tag: 'all', status: 'any', q: '' }
const names = (f: Partial<ClusterPageFilters>) => filterClusters(CLUSTERS.items, { ...base, ...f }).map((c) => c.name)

describe('filterClusters', () => {
  it('filters by the cluster tag id', () => {
    expect(names({ tag: String(TAG_PROD.id) })).toEqual(['core-services'])
    expect(names({ tag: String(TAG_STAGING.id) })).toEqual(['staging-web'])
    expect(names({ tag: String(TAG_DEV.id) })).toHaveLength(4)
    expect(names({ tag: String(QA.id) })).toEqual([])
  })
  it('groups degraded and needs-attention clusters as problems', () => {
    expect(names({ status: 'problems' }).sort()).toEqual(['background-jobs', 'eu-apps'])
    expect(names({ status: 'deploying' })).toEqual(['core-services'])
    expect(names({ status: 'healthy' })).toEqual(['ci-tools', 'edge-proxy', 'staging-web'])
  })
  it('matches name, account and region case-insensitively', () => {
    expect(names({ q: 'EU-APPS' })).toEqual(['eu-apps'])
    expect(names({ q: 'staging-example' })).toEqual(['staging-web'])
    expect(names({ q: 'eu-west-1' })).toEqual(['eu-apps'])
  })
  it('combines facets and treats a blank query as no filter', () => {
    expect(names({ tag: String(TAG_DEV.id), status: 'problems' }).sort()).toEqual(['background-jobs', 'eu-apps'])
    expect(names({ q: '  ' })).toHaveLength(6)
  })
  it('ranks needs attention worst', () => {
    expect(clusterSeverity('needs_attention')).toBeGreaterThan(clusterSeverity('degraded'))
    expect(clusterSeverity('degraded')).toBeGreaterThan(clusterSeverity('deploying'))
    expect(clusterSeverity('deploying')).toBeGreaterThan(clusterSeverity('healthy'))
  })
  it('counts every visible tag, including empty and custom ones, and every status', () => {
    expect(clusterCounts(CLUSTERS.items, VISIBLE)).toEqual({
      tag: { all: 6, '1': 4, '2': 1, '3': 1, '9': 0 },
      status: { any: 6, problems: 2, deploying: 1, healthy: 3 },
    })
  })
  it('counts nothing for a tag the user cannot see', () => {
    expect(clusterCounts(CLUSTERS.items, [QA]).tag).toEqual({ all: 6, '9': 0 })
  })
})

describe('resolveTagFilter', () => {
  it('accepts the id of a visible tag, including a custom one', () => {
    expect(resolveTagFilter('2', VISIBLE)).toBe('2')
    expect(resolveTagFilter('9', VISIBLE)).toBe('9')
  })
  it('maps the legacy prod/staging/dev values to the visible tag with that name, ignoring case', () => {
    expect(resolveTagFilter('prod', VISIBLE)).toBe('3')
    expect(resolveTagFilter('staging', VISIBLE)).toBe('2')
    expect(resolveTagFilter('dev', VISIBLE)).toBe('1')
  })
  it('falls back to all for a legacy name that is not visible, an unknown id, or nothing', () => {
    expect(resolveTagFilter('prod', [TAG_DEV, QA])).toBe('all')
    expect(resolveTagFilter('77', VISIBLE)).toBe('all')
    expect(resolveTagFilter('bogus', VISIBLE)).toBe('all')
    expect(resolveTagFilter(null, VISIBLE)).toBe('all')
    expect(resolveTagFilter('all', VISIBLE)).toBe('all')
  })
})
