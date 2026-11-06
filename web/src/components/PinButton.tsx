import styles from './PinButton.module.css'
import { usePhone } from './usePhone'

interface PinButtonProps {
  /** The service or cluster name, used in the accessible label. */
  name: string
  pinned: boolean
  onToggle: () => void
}

/** Pin or unpin to the sidebar; shared by the service and cluster page headers. */
export function PinButton({ name, pinned, onToggle }: PinButtonProps) {
  // Phones have no sidebar: a pin lands on Overview and Services, as the header sheet says.
  const phone = usePhone()
  const title = phone ? (pinned ? 'Unpin from Overview and Services' : 'Pin to Overview and Services') : pinned ? 'Remove from the sidebar' : 'Pin to the sidebar'
  return (
    <button
      type="button"
      className={styles.pin}
      aria-pressed={pinned}
      aria-label={`${pinned ? 'Unpin' : 'Pin'} ${name}`}
      title={title}
      onClick={onToggle}
    >
      {pinned ? 'Pinned' : 'Pin'}
    </button>
  )
}
