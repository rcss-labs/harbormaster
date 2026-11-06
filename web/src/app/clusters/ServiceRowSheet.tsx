import type { ClusterServiceDTO } from '../../api/types'
import { BottomSheet, SheetAction } from '../../components/BottomSheet'
import { usePins } from '../shell/pins'
import type { TagClickHandler } from './tagTarget'

interface ServiceRowSheetProps {
  /** The service whose row was opened; null while closed. */
  service: ClusterServiceDTO | null
  clusterName: string
  onClose: () => void
  /** Given only for admins; the Edit tag row is left out otherwise. */
  onTagClick?: TagClickHandler
}

/** Actions for one service row of a cluster's Services list on phones. */
export function ServiceRowSheet({ service, clusterName, onClose, onTagClick }: ServiceRowSheetProps) {
  const { isPinned, toggle } = usePins()
  if (!service) return null
  const pinned = isPinned('service', service.id)
  return (
    <BottomSheet open onClose={onClose} label={`Actions for ${service.name}`} title={service.name}
      sub={`Tag ${service.tag.name}, ${service.tag.inherited ? `inherited from cluster ${clusterName}` : 'set on this service'}`}
      role="menu"
    >
      <SheetAction icon="services" label="Open service" href={`/services/${service.id}/overview`} />
      <SheetAction icon="tasks" label="Tasks" href={`/services/${service.id}/tasks`} />
      <SheetAction
        icon="pin"
        label={pinned ? 'Unpin' : 'Pin'}
        onClick={() => {
          toggle({ kind: 'service', id: service.id, name: service.name, cluster: clusterName })
          onClose()
        }}
      />
      {onTagClick && (
        <SheetAction
          icon="tag"
          label="Edit tag"
          onClick={() => {
            onClose()
            onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag })
          }}
        />
      )}
      <SheetAction icon="deploy" label="Deploy" href={`/services/${service.id}/deploy`} />
    </BottomSheet>
  )
}
