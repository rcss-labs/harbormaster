import type { ClusterCardDTO, ClusterDTO } from '../../api/types'
import { BottomSheet, SheetAction } from '../../components/BottomSheet'
import { Icon } from '../../components/Icon'
import { usePins } from '../shell/pins'
import { useAuth } from '../auth/AuthProvider'
import styles from './clusters.module.css'

/** The "…" button at the end of a phone row; opens the row's action sheet. */
export function RowMoreButton({ name, onClick }: { name: string; onClick: () => void }) {
  return (
    <button type="button" className={styles.moreButton} aria-label={`Actions for ${name}`} aria-haspopup="dialog" onClick={onClick}>
      <Icon name="more" size={18} />
    </button>
  )
}

interface ClusterRowSheetProps {
  /** The cluster whose row was opened; null while closed. */
  cluster: ClusterCardDTO | null
  onClose: () => void
  onEditTag: (cluster: ClusterCardDTO) => void
}

/** Actions for one cluster row of the Clusters list on phones. */
export function ClusterRowSheet({ cluster, onClose, onEditTag }: ClusterRowSheetProps) {
  const { me } = useAuth()
  const { isPinned, toggle } = usePins()
  if (!cluster) return null
  const pinned = isPinned('cluster', cluster.id)
  return (
    <BottomSheet open onClose={onClose} label={`Actions for ${cluster.name}`} title={cluster.name} sub={`Tag ${cluster.tag.name}, set on this cluster`} role="menu">
      <SheetAction icon="clusters" label="Open cluster" href={`/clusters/${cluster.id}`} />
      <SheetAction icon="services" label="Services" href={`/services?${new URLSearchParams({ q: cluster.name })}`} />
      <SheetAction icon="tasks" label="Tasks" href={`/tasks?${new URLSearchParams({ filter: 'all', q: cluster.name })}`} />
      <SheetAction
        icon="pin"
        label={pinned ? 'Unpin' : 'Pin'}
        onClick={() => {
          toggle({ kind: 'cluster', id: cluster.id, name: cluster.name, account_name: cluster.account_name, account_id: cluster.account_id })
          onClose()
        }}
      />
      {me?.admin && <SheetAction icon="tag" label="Edit tag" onClick={() => onEditTag(cluster)} />}
    </BottomSheet>
  )
}

interface ClusterHeadSheetProps {
  /** The open cluster detail; the sheet is closed when `open` is false. */
  open: boolean
  cluster: ClusterDTO
  onClose: () => void
  onEditTag: () => void
}

/** The "…" menu of the cluster detail header on phones: Edit tag (admins), Tasks in this cluster, AWS console. */
export function ClusterHeadSheet({ open, cluster, onClose, onEditTag }: ClusterHeadSheetProps) {
  const { me } = useAuth()
  return (
    <BottomSheet open={open} onClose={onClose} label={`Actions for ${cluster.name}`} title={cluster.name} role="menu">
      {me?.admin && <SheetAction icon="tag" label="Edit tag" onClick={onEditTag} />}
      <SheetAction icon="tasks" label="Tasks in this cluster" href={`/tasks?${new URLSearchParams({ filter: 'all', q: cluster.name })}`} />
      <SheetAction
        icon="ext"
        label="Open in AWS console ↗"
        onClick={() => {
          window.open(cluster.console_url, '_blank', 'noopener')
          onClose()
        }}
      />
    </BottomSheet>
  )
}
