import { useState } from 'react'
import type { SecurityAcceptance, SecurityFinding, SecurityImageDetail, Severity } from '../../api/security'
import { useRevokeAcceptance } from '../../api/security'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { Pill, type PillKind } from '../../components/Pill'
import { useToast } from '../../components/Toast'
import { AcceptRiskDialog } from './AcceptRiskDialog'
import { SECURITY_COPY } from './copy'
import { formatDate } from './freshness'
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

interface PendingRevoke {
  vulnId: string
  acceptance: SecurityAcceptance
}

/** The findings of one scanned image, with the accept-risk and revoke controls the caller may use. */
export function FindingsTable({ image }: { image: SecurityImageDetail }) {
  const toast = useToast()
  const revoke = useRevokeAcceptance()
  const [accepting, setAccepting] = useState<SecurityFinding | null>(null)
  const [revoking, setRevoking] = useState<PendingRevoke | null>(null)

  const canAcceptFor = (finding: SecurityFinding) =>
    image.can.accept &&
    image.requests.some((request) => request.can_accept && !finding.acceptances.some((a) => a.service_id === request.service_id))

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
                  : SECURITY_COPY.acceptedForSome(accepted, image.requests.length)}
              </span>
            )}
          </>
        )
      },
    },
    { key: 'id', header: 'Vulnerability', width: '140px', render: (finding) => <span className={styles.mono}>{finding.vuln_id}</span> },
    { key: 'package', header: 'Package', width: 'minmax(120px, 1fr)', render: (finding) => finding.package },
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
        if (!canAcceptFor(finding) && revocable.length === 0) return null
        return (
          <span className={styles.actions}>
            {canAcceptFor(finding) && (
              <button type="button" className={styles.btn} onClick={() => setAccepting(finding)}>
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
      onError: (error) => toast.show(error instanceof Error ? error.message : 'The acceptance could not be revoked.', 'bad'),
      onSettled: () => setRevoking(null),
    })
  }

  return (
    <>
      <DataTable label="Findings" columns={columns} rows={image.findings} rowKey={(finding) => `${finding.vuln_id}|${finding.package}|${finding.version}|${finding.location}`} minWidth={760} />
      {accepting && <AcceptRiskDialog digest={image.digest} finding={accepting} requests={image.requests} onClose={() => setAccepting(null)} />}
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
