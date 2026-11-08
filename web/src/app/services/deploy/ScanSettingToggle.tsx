import { useSecurityService, useSetPreDeployScan } from '../../../api/security'
import { LockNote } from '../../../components/LockNote'
import { useToast } from '../../../components/Toast'
import { Toggle } from '../../../components/Toggle'
import styles from './editors.module.css'

interface ScanSettingToggleProps {
  serviceId: string
  serviceName: string
  /** Name of the service's tag, for the "You need Deploy on ..." lock title. */
  tagName: string
}

/** The per-service "Scan Image for Vulnerabilities Before Deploy" setting. It saves at once; the Deploy tab's drafts are not involved. */
export function ScanSettingToggle({ serviceId, serviceName, tagName }: ScanSettingToggleProps) {
  const security = useSecurityService(serviceId)
  const save = useSetPreDeployScan(serviceId)
  const toast = useToast()
  const setting = security.data
  if (!setting?.enabled) return null

  const lock = setting.can_change_setting ? undefined : `You need Deploy on ${tagName}`
  const checked = save.isPending ? save.variables : setting.pre_deploy_scan
  const change = (next: boolean) =>
    save.mutate(next, {
      onSuccess: () => toast.show(`Pre-deploy image scanning is ${next ? 'on' : 'off'} for ${serviceName}.`, 'ok'),
      onError: (failure) => toast.show(failure instanceof Error ? failure.message : 'The setting could not be saved.', 'bad'),
    })

  return (
    <div className={styles.group}>
      <LockNote>{lock}</LockNote>
      <span title={lock}>
        <Toggle checked={checked} disabled={lock !== undefined || save.isPending} onChange={change}>
          Scan Image for Vulnerabilities Before Deploy
        </Toggle>
      </span>
      <p className={styles.hint}>Rollbacks to a revision that ran before, restarts and redeploys of the running revision are never scanned or delayed.</p>
    </div>
  )
}
