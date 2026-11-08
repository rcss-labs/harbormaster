import { useState } from 'react'
import { useAcceptRisk, type SecurityFinding, type SecurityRequest } from '../../api/security'
import { Dialog } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { SECURITY_COPY } from './copy'
import { formatDate } from './freshness'
import styles from './security.module.css'

const MAX_DAYS = 30
const DAY_MS = 24 * 60 * 60 * 1000

interface AcceptRiskDialogProps {
  digest: string
  finding: SecurityFinding
  requests: SecurityRequest[]
  onClose: () => void
}

/** Accepts one finding for the requesting services the user may accept for; the server still enforces the permission. */
export function AcceptRiskDialog({ digest, finding, requests, onClose }: AcceptRiskDialogProps) {
  const toast = useToast()
  const accept = useAcceptRisk(digest)
  const acceptable = requests.filter((request) => request.can_accept)
  const [selected, setSelected] = useState(() => new Set(acceptable.map((request) => request.service_id)))
  const [reason, setReason] = useState('')
  const [daysText, setDaysText] = useState(String(MAX_DAYS))

  const days = Number(daysText)
  const daysValid = Number.isInteger(days) && days >= 1 && days <= MAX_DAYS
  const canSubmit = reason.trim() !== '' && daysValid && selected.size > 0 && !accept.isPending

  const toggle = (serviceId: string) =>
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(serviceId)) next.delete(serviceId)
      else next.add(serviceId)
      return next
    })

  const submit = () => {
    const serviceIds = acceptable.map((request) => request.service_id).filter((id) => selected.has(id))
    accept.mutate(
      {
        vuln_id: finding.vuln_id,
        package_name: finding.package,
        package_version: finding.version,
        reason: reason.trim(),
        days,
        service_ids: serviceIds,
      },
      {
        onSuccess: () => {
          toast.show(SECURITY_COPY.acceptedToast(finding.vuln_id, formatDate(Date.now() + days * DAY_MS), serviceIds.length), 'ok')
          onClose()
        },
        onError: (error) => toast.show(error instanceof Error ? error.message : 'The risk could not be accepted.', 'bad'),
      },
    )
  }

  return (
    <Dialog
      title={SECURITY_COPY.acceptTitle(finding.vuln_id)}
      sub={`${finding.package} ${finding.version}`}
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
