import styles from './PinButton.module.css'

interface PinButtonProps {
  /** The service or cluster name, used in the accessible label. */
  name: string
  pinned: boolean
  onToggle: () => void
}

/** Pin or unpin to the sidebar; shared by the service and cluster page headers. */
export function PinButton({ name, pinned, onToggle }: PinButtonProps) {
  return (
    <button
      type="button"
      className={styles.pin}
      aria-pressed={pinned}
      aria-label={`${pinned ? 'Unpin' : 'Pin'} ${name}`}
      title={pinned ? 'Remove from the sidebar' : 'Pin to the sidebar'}
      onClick={onToggle}
    >
      {pinned ? 'Pinned' : 'Pin'}
    </button>
  )
}
