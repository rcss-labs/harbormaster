import { describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { clusterCounts, clusterEnv, clusterSeverity, filterClusters, type ClusterPageFilters } from './filterClusters'

const base: ClusterPageFilters = { env: 'all', status: 'any', q: '' }
const names = (f: Partial<ClusterPageFilters>) => filterClusters(CLUSTERS.items, { ...base, ...f }).map((c) => c.name)

describe('filterClusters', () => {
  it('derives the environment from the cluster tag name', () => {
    expect(names({ env: 'prod' })).toEqual(['core-services'])
    expect(names({ env: 'staging' })).toEqual(['staging-web'])
    expect(names({ env: 'dev' })).toHaveLength(4)
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
    expect(names({ env: 'dev', status: 'problems' }).sort()).toEqual(['background-jobs', 'eu-apps'])
    expect(names({ q: '  ' })).toHaveLength(6)
  })
  it('ranks needs attention worst', () => {
    expect(clusterSeverity('needs_attention')).toBeGreaterThan(clusterSeverity('degraded'))
    expect(clusterSeverity('degraded')).toBeGreaterThan(clusterSeverity('deploying'))
    expect(clusterSeverity('deploying')).toBeGreaterThan(clusterSeverity('healthy'))
  })
  it('returns no environment for custom tags', () => {
    expect(clusterEnv({ ...CLUSTERS.items[0], tag: { ...CLUSTERS.items[0].tag, name: 'payments' } })).toBeNull()
  })
  it('counts every environment and status over the given clusters', () => {
    expect(clusterCounts(CLUSTERS.items)).toEqual({
      env: { all: 6, prod: 1, staging: 1, dev: 4 },
      status: { any: 6, problems: 2, deploying: 1, healthy: 3 },
    })
  })
})
