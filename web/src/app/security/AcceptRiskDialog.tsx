import { useState } from 'react'
import { useAcceptPackageRisk, useAcceptRisk, type SecurityFinding, type SecurityRequest } from '../../api/security'
import { Dialog } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { SECURITY_COPY } from './copy'
import { formatDate } from './freshness'
import type { PackageGroup } from './packages'
import styles from './security.module.css'

const MAX_DAYS = 30
const DAY_MS = 24 * 60 * 60 * 1000

const uniqueByService = (requests: SecurityRequest[]) => [...new Map(requests.map((request) => [request.service_id, request])).values()]

/** The services the user may still accept a finding for: they can accept and have no acceptance of it yet. */
export const servicesToAccept = (finding: SecurityFinding, requests: SecurityRequest[]) =>
  uniqueByService(requests).filter((request) => request.can_accept && !finding.acceptances.some((a) => a.service_id === request.service_id))

/** The services the user may still accept some vulnerability of the package for. */
export const servicesToAcceptPackage = (group: PackageGroup, requests: SecurityRequest[]) =>
  uniqueByService(requests).filter((request) => group.findings.some((finding) => servicesToAccept(finding, [request]).length > 0))

export type AcceptTarget = { kind: 'finding'; finding: SecurityFinding } | { kind: 'package'; group: PackageGroup }

interface AcceptRiskDialogProps {
  digest: string
  target: AcceptTarget
  requests: SecurityRequest[]
  onClose: () => void
}

/**
 * Accepts one finding, or every vulnerability of a package, for the requesting services the user may accept for; the
 * server still enforces the permission and skips what a service already accepted.
 */
export function AcceptRiskDialog({ digest, target, requests, onClose }: AcceptRiskDialogProps) {
  const toast = useToast()
  const acceptFinding = useAcceptRisk(digest)
  const acceptPackage = useAcceptPackageRisk(digest)
  const pending = acceptFinding.isPending || acceptPackage.isPending
  // The single-finding endpoint rejects the whole request when a chosen service already holds an acceptance, so those are left out.
  const acceptable = target.kind === 'finding' ? servicesToAccept(target.finding, requests) : servicesToAcceptPackage(target.group, requests)
  const [selected, setSelected] = useState(() => new Set(acceptable.map((request) => request.service_id)))
  const [reason, setReason] = useState('')
  const [daysText, setDaysText] = useState(String(MAX_DAYS))

  const days = Number(daysText)
  const daysValid = Number.isInteger(days) && days >= 1 && days <= MAX_DAYS
  const canSubmit = reason.trim() !== '' && daysValid && selected.size > 0 && !pending

  const toggle = (serviceId: string) =>
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(serviceId)) next.delete(serviceId)
      else next.add(serviceId)
      return next
    })

  const submit = () => {
    const serviceIds = acceptable.map((request) => request.service_id).filter((id) => selected.has(id))
    const until = formatDate(Date.now() + days * DAY_MS)
    const callbacks = (success: string) => ({
      onSuccess: () => {
        toast.show(success, 'ok')
        onClose()
      },
      onError: (error: Error) => toast.show(error.message || SECURITY_COPY.acceptFailed, 'bad'),
    })
    const terms = { reason: reason.trim(), days, service_ids: serviceIds }
    if (target.kind === 'finding') {
      const { finding } = target
      acceptFinding.mutate(
        { vuln_id: finding.vuln_id, package_name: finding.package, package_version: finding.version, ...terms },
        callbacks(SECURITY_COPY.acceptedToast(finding.vuln_id, until, serviceIds.length)),
      )
    } else {
      acceptPackage.mutate({ package_name: target.group.name, ...terms }, callbacks(SECURITY_COPY.packageAcceptedToast(target.group.name, until, serviceIds.length)))
    }
  }

  const [title, sub] =
    target.kind === 'finding'
      ? [SECURITY_COPY.acceptTitle(target.finding.vuln_id), `${target.finding.package} ${target.finding.version}`]
      : [SECURITY_COPY.acceptPackageTitle(target.group.name), SECURITY_COPY.acceptPackageSub(target.group.findings.length, target.group.versions)]

  return (
    <Dialog
      title={title}
      sub={sub}
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: SECURITY_COPY.acceptSubmit(daysValid ? days : MAX_DAYS), kind: 'primary', disabled: !canSubmit, onClick: submit },
      ]}
    >
      <fieldset className={styles.services}>
        <legend>{SECURITY_COPY.acceptServicesLegend}</legend>
        {acceptable.map((request) => (
          <label key={request.service_id}>
            <input type="checkbox" checked={selected.has(request.service_id)} onChange={() => toggle(request.service_id)} />
            {request.service}
          </label>
        ))}
      </fieldset>
      <p className={styles.note}>{SECURITY_COPY.acceptServicesNote}</p>
      <label className={styles.field}>
        {SECURITY_COPY.acceptReason}
        <textarea value={reason} onChange={(event) => setReason(event.target.value)} required />
      </label>
      <label className={styles.field}>
        {SECURITY_COPY.acceptDays}
        <input type="number" min={1} max={MAX_DAYS} value={daysText} onChange={(event) => setDaysText(event.target.value)} />
      </label>
    </Dialog>
  )
}
