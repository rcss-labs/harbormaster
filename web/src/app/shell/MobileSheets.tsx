import { useDataProblems } from '../../api/observe'
import { usePollerStatus } from '../../api/hooks'
import { BottomSheet } from '../../components/BottomSheet'
import { StatusDot } from '../../components/StatusDot'
import { useAuth } from '../auth/AuthProvider'
import { anyStale, FreshnessRegions, nonCoreNote, worstKind } from './FreshnessControl'
import { ScopeBody } from './ScopeControl'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

/** Phone scope sheet: the same fields and saved scopes as the desktop popover, with Clear scope and Done at the foot. */
export function ScopeSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { setAccount, setRegion, setTags } = useFilters()
  const clear = () => {
    setAccount(undefined)
    setRegion(undefined)
    setTags([])
  }
  return (
    <BottomSheet
      open={open}
      onClose={onClose}
      label="Scope"
      title="Scope"
      closeLabel="Close scope"
      footer={
        <>
          <button type="button" className={styles.sheetBtn} onClick={clear}>
            Clear scope
          </button>
          <button type="button" className={`${styles.sheetBtn} ${styles.sheetBtnPrimary}`} onClick={onClose}>
            Done
          </button>
        </>
      }
    >
      <ScopeBody />
    </BottomSheet>
  )
}

/** Phone data-freshness sheet: the sentence the chip abbreviates, then the same region rows as the desktop popover. */
export function FreshnessSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { me } = useAuth()
  const poller = usePollerStatus()
  const { current, total } = useDataProblems()
  const items = poller.data?.items ?? []
  const note = nonCoreNote(items)
  return (
    <BottomSheet
      open={open}
      onClose={onClose}
      label="Data freshness"
      title="Data freshness"
      sub={<StatusDot kind={worstKind(items)}>{`${current} of ${total} regions current`}</StatusDot>}
    >
      <div className={styles.freshPop}>
        <FreshnessRegions items={items} admin={me?.admin ?? false} stale={anyStale(items)} onNavigate={onClose} />
        {note && <span className={styles.freshCurrent}>{note}</span>}
      </div>
    </BottomSheet>
  )
}
