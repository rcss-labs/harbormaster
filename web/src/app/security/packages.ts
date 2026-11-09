import type { SecurityFinding, Severity } from '../../api/security'

export const SEVERITY_RANK: Record<Severity, number> = { critical: 0, high: 1, medium: 2, low: 3, negligible: 4, unknown: 5 }

/** Package types of operating system package managers; everything else is a language or app dependency. */
const OS_PACKAGE_TYPES = new Set(['deb', 'apk', 'rpm', 'alpm', 'portage', 'nix'])

export type PackageKind = 'os' | 'app'

/** The findings of one package name across every version of it in the image. */
export interface PackageGroup {
  name: string
  /** Guessed from the package type: OS packages usually come from the base image. */
  kind: PackageKind
  /** Worst severity among the findings. */
  severity: Severity
  /** Installed versions, newest first. */
  versions: string[]
  /** The newest fixed version among the findings: upgrading to it fixes every finding that has a fix. Empty when none has. */
  fixVersion: string
  /** Findings without a fix yet. */
  unfixed: number
  /** Worst first, then by vulnerability id. */
  findings: SecurityFinding[]
}

// dpkg's ordering of a non-digit run: "~" sorts before everything, even the end of the string; letters before other characters.
const charOrder = (char: string | undefined) => {
  if (char === undefined) return 0
  if (char === '~') return -1
  if (/[A-Za-z]/.test(char)) return char.charCodeAt(0)
  return char.charCodeAt(0) + 256
}

// Compares two versions part by part the way dpkg does: alternating non-digit runs and numbers.
function compareParts(a: string, b: string): number {
  let i = 0
  let j = 0
  while (i < a.length || j < b.length) {
    while ((i < a.length && !/\d/.test(a[i])) || (j < b.length && !/\d/.test(b[j]))) {
      const left = i < a.length && !/\d/.test(a[i]) ? a[i] : undefined
      const right = j < b.length && !/\d/.test(b[j]) ? b[j] : undefined
      const diff = charOrder(left) - charOrder(right)
      if (diff !== 0) return diff
      if (left !== undefined) i++
      if (right !== undefined) j++
    }
    let leftNumber = ''
    let rightNumber = ''
    while (i < a.length && /\d/.test(a[i])) leftNumber += a[i++]
    while (j < b.length && /\d/.test(b[j])) rightNumber += b[j++]
    const diff = Number(leftNumber || '0') - Number(rightNumber || '0')
    if (diff !== 0) return diff
  }
  return 0
}

const splitEpoch = (version: string): [number, string] => {
  const match = /^(\d+):(.*)$/.exec(version)
  return match ? [Number(match[1]), match[2]] : [0, version]
}

/** Orders versions of Debian, Alpine, RPM and language packages alike: an epoch first, then dpkg's comparison. */
export function compareVersions(a: string, b: string): number {
  const [epochA, restA] = splitEpoch(a)
  const [epochB, restB] = splitEpoch(b)
  return epochA !== epochB ? epochA - epochB : compareParts(restA, restB)
}

const newest = (versions: string[]) => versions.reduce((best, version) => (best === '' || compareVersions(version, best) > 0 ? version : best), '')

/** One group per package name, the worst first; a tie goes to the package with more findings, then to its name. */
export function groupByPackage(findings: SecurityFinding[]): PackageGroup[] {
  const byName = new Map<string, SecurityFinding[]>()
  for (const finding of findings) byName.set(finding.package, [...(byName.get(finding.package) ?? []), finding])

  const groups = [...byName].map(([name, members]): PackageGroup => {
    const sorted = [...members].sort((a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || a.vuln_id.localeCompare(b.vuln_id))
    return {
      name,
      kind: members.some((f) => OS_PACKAGE_TYPES.has(f.type)) ? 'os' : 'app',
      severity: sorted[0].severity,
      versions: [...new Set(members.map((f) => f.version))].sort((a, b) => compareVersions(b, a)),
      fixVersion: newest(members.map((f) => f.fixed_version).filter((v) => v !== '')),
      unfixed: members.filter((f) => f.fixed_version === '').length,
      findings: sorted,
    }
  })
  return groups.sort(
    (a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || b.findings.length - a.findings.length || a.name.localeCompare(b.name),
  )
}
