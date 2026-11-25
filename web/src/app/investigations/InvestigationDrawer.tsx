import { useInvestigation } from '../../api/investigations'
import { Drawer } from '../../components/Drawer'
import { Skeleton } from '../../components/Skeleton'
import { PanelError } from '../../components/PanelError'
import { InvestigationView } from './InvestigationView'

/** An investigation in the app's drawer (a bottom sheet on phones). The id is the one from the list, the alert or the URL. */
export function InvestigationDrawer({ id, onClose }: { id: number; onClose: () => void }) {
  const query = useInvestigation(id)
  return (
    <Drawer open onClose={onClose} title="Investigation" wide>
      {query.data ? (
        <InvestigationView investigation={query.data} />
      ) : query.isError ? (
        <PanelError message="The investigation could not be read." onRetry={() => void query.refetch()} />
      ) : (
        <Skeleton />
      )}
    </Drawer>
  )
}
