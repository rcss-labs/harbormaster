import { useState } from 'react'
import { Link } from 'react-router'
import type { ServiceDTO } from '../../api/types'
import { BottomSheet, SheetAction } from '../../components/BottomSheet'
import { Icon } from '../../components/Icon'
import { PinButton } from '../../components/PinButton'
import { StatusDot } from '../../components/StatusDot'
import { TagChip } from '../../components/TagChip'
import { usePhone } from '../../components/usePhone'
import type { TagClickHandler } from '../clusters/tagTarget'
import { usePins } from '../shell/pins'
import { TAB_LOCKS, isUnlocked } from './tabs'
import { DesiredCountSheet, HeaderActions } from './HeaderActions'
import { RestartDialog, restartBlockedTitle } from './RestartDialog'
import { ScanImagesButton, ScanImagesDialog, useScanningEnabled } from './ScanImagesButton'
import styles from './service.module.css'

interface ServiceHeaderProps {
  service: ServiceDTO
  /** Opens the tag dialog; passed only for admins. */
  onTagClick?: TagClickHandler
  /** The open tab; the phone row leaves out its Deploy link on the Deploy tab. */
  tab?: string
}

export function ServiceHeader({ service, onTagClick, tab }: ServiceHeaderProps) {
  const phone = usePhone()
  const { isPinned, toggle } = usePins()
  const pinned = isPinned('service', service.id)
  const launchType = service.launch_type === 'FARGATE' ? 'Fargate' : service.launch_type
  const tagChip = (
    <TagChip
      tag={service.tag}
      kind="service"
      inheritedFrom={service.tag.inherited ? service.cluster.name : undefined}
      onClick={onTagClick && (() => onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag }))}
    />
  )
  const togglePin = () => toggle({ kind: 'service', id: service.id, name: service.name, cluster: service.cluster.name })

  if (phone) {
    return <PhoneServiceHeader service={service} tab={tab} launchType={launchType} tagChip={tagChip} pinned={pinned} onTogglePin={togglePin} />
  }

  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <div className={styles.titleRow}>
          <h1>{service.name}</h1>
          {tagChip}
          <StatusDot status={service.status} />
          <PinButton name={service.name} pinned={pinned} onToggle={togglePin} />
        </div>
        <div className={styles.meta}>
          <Link className={styles.accentLink} to={`/clusters/${service.cluster.id}`}>
            {service.cluster.name}
          </Link>
          <span>{service.account_name}</span>
          <span>{service.region}</span>
          <span className={styles.mono}>
            {service.family}:{service.revision}
          </span>
          <span>{launchType}</span>
        </div>
      </div>
      <div className={styles.actions}>
        <ScanImagesButton service={service} />
        {isUnlocked(TAB_LOCKS.deploy!, service.can) ? (
          <Link className={styles.btnPrimary} to={`/services/${service.id}/deploy`}>
            Deploy
          </Link>
        ) : (
          <button type="button" className={styles.btnPrimary} disabled title={`You need ${TAB_LOCKS.deploy!.action} on ${service.tag.name}`}>
            Deploy
          </button>
        )}
        <HeaderActions serviceId={service.id} service={service} />
      </div>
    </div>
  )
}

type PhoneSheet = 'actions' | 'restart' | 'desired' | 'scan'

interface PhoneServiceHeaderProps {
  service: ServiceDTO
  tab?: string
  launchType: string
  tagChip: React.ReactNode
  pinned: boolean
  onTogglePin: () => void
}

/** The sticky compact row under the phone top bar: status, tag, revision, launch type, Deploy and a "…" sheet with the other actions. */
function PhoneServiceHeader({ service, tab, launchType, tagChip, pinned, onTogglePin }: PhoneServiceHeaderProps) {
  const [sheet, setSheet] = useState<PhoneSheet | null>(null)
  const deployLock = TAB_LOCKS.deploy!
  const canDeploy = isUnlocked(deployLock, service.can)
  const tagName = service.tag.name
  const restartBlocked = restartBlockedTitle(service)
  const canOperate = service.can.operate
  const scanning = useScanningEnabled(service.id)
  const close = () => setSheet(null)

  return (
    <div className={styles.phoneHead}>
      <h1 className="hm-phone-title-sr">{service.name}</h1>
      <div className={styles.phoneMeta}>
        <StatusDot status={service.status} />
        {tagChip}
        <Link className={`${styles.mono} ${styles.revisionLink}`} to={`/services/${service.id}/task-definition`}>
          :{service.revision}
        </Link>
        <span className={styles.phoneLaunch}>{launchType}</span>
        <span className={styles.grow} />
        {canDeploy && tab !== 'deploy' && (
          <Link className={styles.phoneDeploy} to={`/services/${service.id}/deploy`}>
            <Icon name="deploy" size={16} />
            Deploy
          </Link>
        )}
        <button type="button" className={styles.phoneMore} aria-label={`More actions for ${service.name}`} aria-haspopup="menu" onClick={() => setSheet('actions')}>
          <Icon name="more" size={18} />
        </button>
      </div>
      <BottomSheet
        open={sheet === 'actions'}
        onClose={close}
        role="menu"
        label={`More actions for ${service.name}`}
        title={service.name}
        sub={`${service.cluster.name} · ${service.account_name} · ${service.region}`}
      >
        <SheetAction icon="restart" label="Restart" why={restartBlocked} disabled={restartBlocked !== undefined} onClick={() => setSheet('restart')} />
        <SheetAction
          icon="plus"
          label="Change desired count"
          why={canOperate ? `Now ${service.desired}.` : `Now ${service.desired}. Needs Operate on ${tagName}.`}
          disabled={!canOperate}
          onClick={() => setSheet('desired')}
        />
        {scanning && (
          <SheetAction
            icon="shield"
            label="Scan image"
            why={service.can.deploy ? undefined : `You need Deploy on ${tagName}`}
            disabled={!service.can.deploy}
            onClick={() => setSheet('scan')}
          />
        )}
        <SheetAction
          icon="deploy"
          label="Deploy"
          href={`/services/${service.id}/deploy`}
          why={canDeploy ? undefined : `You need ${deployLock.action} on ${tagName}`}
          disabled={!canDeploy}
          onClick={close}
        />
        <SheetAction
          icon="pin"
          label={pinned ? 'Unpin from Overview and Services' : 'Pin to Overview and Services'}
          onClick={() => {
            onTogglePin()
            close()
          }}
        />
        <SheetAction
          icon="ext"
          label="AWS console"
          onClick={() => {
            window.open(service.console_url, '_blank', 'noopener')
            close()
          }}
        />
      </BottomSheet>
      {sheet === 'restart' && <RestartDialog serviceId={service.id} service={service} onClose={close} />}
      {sheet === 'scan' && <ScanImagesDialog service={service} onClose={close} />}
      {sheet === 'desired' && <DesiredCountSheet serviceId={service.id} service={service} onClose={close} />}
    </div>
  )
}
