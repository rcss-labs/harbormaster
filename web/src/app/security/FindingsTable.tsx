import { useMemo, useState, type MouseEvent } from 'react'
import type { SecurityAcceptance, SecurityFinding, SecurityImageDetail, Severity } from '../../api/security'
import { useRevokeAcceptance } from '../../api/security'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { Pill, type PillKind } from '../../components/Pill'
import { useToast } from '../../components/Toast'
import { AcceptRiskDialog, servicesToAccept, servicesToAcceptPackage, type AcceptTarget } from './AcceptRiskDialog'
import { SECURITY_COPY } from './copy'
import { formatDate } from './freshness'
import { groupByPackage, type PackageGroup } from './packages'
import styles from './security.module.css'

const SEVERITY_KIND: Record<Severity, PillKind> = {
  critical: 'bad',
  high: 'warn',
  medium: 'info',
  low: 'mute',
  negligible: 'mute',
  unknown: 'mute',
}

const capitalize = (word: string) => word.charAt(0).toUpperCase() + word.slice(1)

const earliestExpiry = (acceptances: SecurityAcceptance[]) => acceptances.map((a) => a.expires_at).sort()[0]

/** One line per service: who accepted it, until when and why. */
const acceptanceTitle = (acceptances: SecurityAcceptance[]) =>
  acceptances.map((a) => SECURITY_COPY.acceptanceLine(a.service, a.by, formatDate(a.expires_at), a.reason)).join('\n')

const INTERACTIVE = 'a, button, input, select, textarea'

interface PendingRevoke {
  vulnId: string
  acceptance: SecurityAcceptance
}

/** How much of a package is accepted: everything (until the first acceptance ends), some vulnerabilities, or nothing. */
function packageNote(group: PackageGroup): string | null {
  const everywhere = group.findings.filter((finding) => finding.accepted_everywhere)
  if (everywhere.length === group.findings.length) return SECURITY_COPY.acceptedUntil(formatDate(earliestExpiry(group.findings.flatMap((f) => f.acceptances))))
  if (everywhere.length > 0) return SECURITY_COPY.packagesAccepted(everywhere.length, group.findings.length)
  return group.findings.some((finding) => finding.acceptances.length > 0) ? SECURITY_COPY.partlyAccepted : null
}

/**
 * The findings of one scanned image, one row per package with the version that fixes them all. Opening a package lists
 * its vulnerabilities with the per-finding accept-risk and revoke controls; the package row accepts all of them at once.
 */
export function FindingsTable({ image }: { image: SecurityImageDetail }) {
  const toast = useToast()
  const revoke = useRevokeAcceptance()
  const requestingServices = new Set(image.requests.map((request) => request.service_id)).size
  const groups = useMemo(() => groupByPackage(image.findings), [image.findings])
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set())
  const [accepting, setAccepting] = useState<AcceptTarget | null>(null)
  const [revoking, setRevoking] = useState<PendingRevoke | null>(null)

  const canAcceptFinding = (finding: SecurityFinding) => image.can.accept && servicesToAccept(finding, image.requests).length > 0
  const canAcceptPackage = (group: PackageGroup) => image.can.accept && servicesToAcceptPackage(group, image.requests).length > 0

  const toggle = (name: string) =>
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  // The whole row opens the package, except clicks on its own controls.
  const onRowClick = (event: MouseEvent<HTMLDivElement>, name: string) => {
    if (!(event.target as HTMLElement).closest(INTERACTIVE)) toggle(name)
  }

  const columns: DataTableColumn<SecurityFinding>[] = [
    {
      key: 'severity',
      header: 'Severity',
      width: 'minmax(150px, 1.2fr)',
      render: (finding) => {
        const accepted = finding.acceptances.length
        return (
          <>
            <Pill kind={SEVERITY_KIND[finding.severity]}>{capitalize(finding.severity)}</Pill>
            {accepted > 0 && (
              <span className={styles.findingNote} title={acceptanceTitle(finding.acceptances)}>
                {finding.accepted_everywhere
                  ? SECURITY_COPY.acceptedUntil(formatDate(earliestExpiry(finding.acceptances)))
                  : SECURITY_COPY.acceptedForSome(new Set(finding.acceptances.map((a) => a.service_id)).size, requestingServices)}
              </span>
            )}
          </>
        )
      },
    },
    {
      key: 'id',
      header: 'Vulnerability',
      width: 'minmax(150px, 1.2fr)',
      render: (finding) =>
        finding.url ? (
          <a className={styles.mono} href={finding.url} target="_blank" rel="noopener noreferrer">
            {finding.vuln_id}
          </a>
        ) : (
          <span className={styles.mono}>{finding.vuln_id}</span>
        ),
    },
    { key: 'version', header: 'Version', width: 'minmax(110px, 1fr)', render: (finding) => <span className={styles.mono}>{finding.version}</span> },
    {
      key: 'fixed',
      header: 'Fixed in',
      width: 'minmax(110px, 1fr)',
      render: (finding) => (finding.fixed_version === '' ? <span className={styles.sub}>{SECURITY_COPY.noFix}</span> : <span className={styles.mono}>{finding.fixed_version}</span>),
    },
    {
      key: 'actions',
      header: '',
      width: 'minmax(110px, 1fr)',
      phone: 'action',
      render: (finding) => {
        const revocable = finding.acceptances.filter((a) => a.can_revoke)
        if (!canAcceptFinding(finding) && revocable.length === 0) return null
        return (
          <span className={styles.actions}>
            {canAcceptFinding(finding) && (
              <button type="button" className={styles.btn} onClick={() => setAccepting({ kind: 'finding', finding })}>
                {SECURITY_COPY.acceptRisk}
              </button>
            )}
            {revocable.map((acceptance) => (
              <button
                key={acceptance.id}
                type="button"
                className={styles.btn}
                title={SECURITY_COPY.revokeTitle(finding.vuln_id, acceptance.service)}
                onClick={() => setRevoking({ vulnId: finding.vuln_id, acceptance })}
              >
                {SECURITY_COPY.revoke}
              </button>
            ))}
          </span>
        )
      },
    },
  ]

  const confirmRevoke = () => {
    if (!revoking) return
    const { vulnId, acceptance } = revoking
    revoke.mutate(acceptance.id, {
      onSuccess: () => toast.show(SECURITY_COPY.revokedToast(vulnId, acceptance.service), 'ok'),
      onError: (error) => toast.show(error instanceof Error ? error.message : SECURITY_COPY.revokeFailed, 'bad'),
      onSettled: () => setRevoking(null),
    })
  }

  return (
    <>
      <div className={styles.packages}>
        <div className={styles.packageHead} aria-hidden="true">
          <span>Severity</span>
          <span>Package</span>
          <span>Installed</span>
          <span>Fix: upgrade to</span>
          <span />
        </div>
        <div role="list" aria-label={SECURITY_COPY.findingsLabel}>
          {groups.map((group) => {
            const open = expanded.has(group.name)
            const bodyId = `package-${group.name.replace(/[^A-Za-z0-9_-]/g, '_')}`
            const note = packageNote(group)
            return (
              <div role="listitem" key={group.name} className={styles.package}>
                <div className={styles.packageRow} onClick={(event) => onRowClick(event, group.name)}>
                  <span>
                    <Pill kind={SEVERITY_KIND[group.severity]}>{capitalize(group.severity)}</Pill>
                    <span className={styles.findingNote}>{SECURITY_COPY.vulnerabilityCount(group.findings.length)}</span>
                    {note && <span className={styles.findingNote}>{note}</span>}
                  </span>
                  <span>
                    <button type="button" className={styles.packageToggle} aria-expanded={open} aria-controls={bodyId} onClick={() => toggle(group.name)}>
                      <span className={styles.chevron} data-open={open} aria-hidden="true">
                        ▸
                      </span>
                      {group.name}
                    </button>
                    <span className={styles.findingNote} title={SECURITY_COPY.packageKindTitle[group.kind]}>
                      {SECURITY_COPY.packageKind[group.kind]}
                    </span>
                  </span>
                  <span className={styles.mono}>{group.versions.join(', ')}</span>
                  <span>
                    {group.fixVersion === '' ? <span className={styles.sub}>{SECURITY_COPY.noFix}</span> : <span className={styles.mono}>{group.fixVersion}</span>}
                    {group.fixVersion !== '' && group.unfixed > 0 && <span className={styles.findingNote}>{SECURITY_COPY.withoutFix(group.unfixed)}</span>}
                  </span>
                  <span className={styles.actions}>
                    {canAcceptPackage(group) && (
                      <button type="button" className={styles.btn} onClick={() => setAccepting({ kind: 'package', group })}>
                        {SECURITY_COPY.acceptPackageRisk}
                      </button>
                    )}
                  </span>
                </div>
                {open && (
                  <div id={bodyId} className={styles.packageBody}>
                    <DataTable
                      label={SECURITY_COPY.vulnerabilitiesIn(group.name)}
                      columns={columns}
                      rows={group.findings}
                      rowKey={(finding) => `${finding.vuln_id}|${finding.version}|${finding.location}`}
                      minWidth={640}
                    />
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
      {accepting && <AcceptRiskDialog digest={image.digest} target={accepting} requests={image.requests} onClose={() => setAccepting(null)} />}
      {revoking && (
        <ConfirmDialog
          title={SECURITY_COPY.revokeTitle(revoking.vulnId, revoking.acceptance.service)}
          confirmLabel={SECURITY_COPY.revoke}
          danger
          confirmDisabled={revoke.isPending}
          onConfirm={confirmRevoke}
          onCancel={() => setRevoking(null)}
        >
          {SECURITY_COPY.revokeBody(revoking.vulnId, revoking.acceptance.service)}
        </ConfirmDialog>
      )}
    </>
  )
}
