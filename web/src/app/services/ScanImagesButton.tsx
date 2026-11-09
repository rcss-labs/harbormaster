import { useState } from 'react'
import { useTaskDefinition } from '../../api/change'
import { useSecurityService } from '../../api/security'
import type { ServiceDTO } from '../../api/types'
import { Dialog } from '../../components/Dialog'
import { ScanImageStatus } from './deploy/ScanImageStatus'
import { containersOf, imageTagOf } from './task-definition/mainContainer'
import styles from './service.module.css'
import scanStyles from './ScanImagesButton.module.css'

/** Whether image scanning is on for the service; the header leaves its Scan controls out while it is off. */
export function useScanningEnabled(serviceId: string) {
  return useSecurityService(serviceId).data?.enabled === true
}

/** "Scan image" for the service header: opens a dialog that scans the deployed tag of each container. Hidden while scanning is off. */
export function ScanImagesButton({ service }: { service: ServiceDTO }) {
  const enabled = useScanningEnabled(service.id)
  const [open, setOpen] = useState(false)
  if (!enabled) return null
  const lock = service.can.deploy ? undefined : `You need Deploy on ${service.tag.name}`
  return (
    <>
      <button type="button" className={styles.btn} disabled={lock !== undefined} title={lock} onClick={() => setOpen(true)}>
        Scan image
      </button>
      {open && <ScanImagesDialog service={service} onClose={() => setOpen(false)} />}
    </>
  )
}

/** One scan control per container of the deployed task definition, each for the tag that runs now. */
export function ScanImagesDialog({ service, onClose }: { service: ServiceDTO; onClose: () => void }) {
  const taskDefinition = useTaskDefinition(service.id)
  const containers = taskDefinition.data ? containersOf(taskDefinition.data.definition) : []
  return (
    <Dialog title="Scan for vulnerabilities" sub={`The image each container of ${service.name} runs now.`} onClose={onClose} actions={[{ label: 'Close', onClick: onClose }]}>
      {taskDefinition.isError && <p className={scanStyles.note}>The task definition could not be loaded.</p>}
      {taskDefinition.isPending && <p className={scanStyles.note}>Loading…</p>}
      <ul className={scanStyles.containers}>
        {containers.map((container) => {
          const name = typeof container.name === 'string' ? container.name : ''
          const image = typeof container.image === 'string' ? container.image : ''
          const tag = imageTagOf(image)
          return (
            <li key={name} className={scanStyles.container}>
              <span className={scanStyles.name}>{name}</span>
              <span className={scanStyles.image}>{image}</span>
              <ScanImageStatus serviceId={service.id} container={name} tag={tag} canDeploy={service.can.deploy} tagName={service.tag.name} />
            </li>
          )
        })}
      </ul>
    </Dialog>
  )
}
