import { describe, expect, it } from 'vitest'
import type { SecurityFinding } from '../../api/security'
import { SECURITY_IMAGE_EDGE } from '../../test/securityFixtures'
import { compareVersions, groupByPackage, packageOrigin } from './packages'

const base = SECURITY_IMAGE_EDGE.findings[2]
const finding = (over: Partial<SecurityFinding>): SecurityFinding => ({ ...base, acceptances: [], accepted_everywhere: false, ...over })

describe('compareVersions', () => {
  it.each([
    ['1.4.1', '1.4.0', 1],
    ['1.10.0', '1.9.9', 1],
    ['0.10.0', '0.7.6', 1],
    ['2.6.2', '2.6.2', 0],
    ['1:2.38.1-5', '2.40', 1], // an epoch wins over any version without one
    ['3.0.15-1', '3.0.15', 1],
    ['1.0~rc1', '1.0', -1], // a tilde sorts before the release
    ['7.30.1', '7.30.0', 1],
  ])('compares %s with %s', (a, b, want) => {
    expect(Math.sign(compareVersions(a, b))).toBe(want)
  })
})

describe('groupByPackage', () => {
  it('folds the findings of a package, in any version, into one row with the version that fixes them all', () => {
    const groups = groupByPackage([
      finding({ vuln_id: 'GHSA-a', package: 'node-forge', version: '0.7.6', severity: 'high', fixed_version: '0.10.0' }),
      finding({ vuln_id: 'GHSA-b', package: 'node-forge', version: '1.3.1', severity: 'critical', fixed_version: '1.4.1' }),
      finding({ vuln_id: 'GHSA-c', package: 'node-forge', version: '0.7.6', severity: 'low', fixed_version: '' }),
      finding({ vuln_id: 'CVE-1', package: 'zlib', version: '1.2.11', severity: 'medium', fixed_version: '1.2.13' }),
    ])
    expect(groups.map((group) => group.name)).toEqual(['node-forge', 'zlib'])
    const [forge] = groups
    expect(forge.severity).toBe('critical')
    expect(forge.versions).toEqual(['1.3.1', '0.7.6'])
    expect(forge.fixVersion).toBe('1.4.1')
    expect(forge.unfixed).toBe(1)
    expect(forge.findings.map((f) => f.vuln_id)).toEqual(['GHSA-b', 'GHSA-a', 'GHSA-c'])
  })

  it('has no fix version when no finding of the package has a fix', () => {
    const [group] = groupByPackage([finding({ package: 'xmldom', fixed_version: '' }), finding({ vuln_id: 'CVE-2', package: 'xmldom', fixed_version: '' })])
    expect(group.fixVersion).toBe('')
    expect(group.unfixed).toBe(2)
  })

  it('orders packages by worst severity, then by how many findings they carry', () => {
    const groups = groupByPackage([
      finding({ package: 'a', severity: 'high' }),
      finding({ package: 'b', severity: 'high' }),
      finding({ vuln_id: 'CVE-2', package: 'b', severity: 'low' }),
      finding({ package: 'c', severity: 'critical' }),
    ])
    expect(groups.map((group) => group.name)).toEqual(['c', 'b', 'a'])
  })
})

describe('packageOrigin', () => {
  const group = (...origins: SecurityFinding['origin'][]) =>
    groupByPackage(origins.map((origin, index) => finding({ vuln_id: `CVE-${index}`, package: 'pkg', origin })))[0]

  it('is base when every finding came from the base image', () => {
    expect(packageOrigin(group('base', 'base'), 'matched')).toBe('base')
  })

  it('is app when every finding came from the app layers', () => {
    expect(packageOrigin(group('app'), 'matched')).toBe('app')
  })

  it('is both when the findings come from both', () => {
    expect(packageOrigin(group('base', 'app'), 'matched')).toBe('both')
  })

  it('ignores findings with an unknown origin', () => {
    expect(packageOrigin(group('', 'app'), 'matched')).toBe('app')
  })

  it('is null when no finding has an origin, so the guess stays', () => {
    expect(packageOrigin(group('', ''), 'matched')).toBeNull()
  })

  it.each(['none', 'pending', 'mismatch', 'error'] as const)('is null when the base image state is %s', (state) => {
    expect(packageOrigin(group('base'), state)).toBeNull()
  })
})
