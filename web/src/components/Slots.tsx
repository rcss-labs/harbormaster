import type { SlotDTO } from '../api/types'
import styles from './Slots.module.css'

function slotColor(slot: SlotDTO): string {
  if (slot.rolling_out) return 'var(--info)'
  return slot.health === 'HEALTHY' ? 'var(--success)' : 'var(--warning)'
}

export function Slots({ slots, desired }: { slots: SlotDTO[]; desired: number }) {
  const missing = Math.max(0, desired - slots.length)
  return (
    <div className={styles.slots}>
      {slots.map((slot) => (
        <span
          key={slot.task_id}
          className={styles.slot}
          style={{ background: slotColor(slot) }}
          title={`${slot.short_id} · ${slot.private_ip}`}
        />
      ))}
      {Array.from({ length: missing }, (_, index) => (
        <span key={`missing-${index}`} className={`${styles.slot} ${styles.empty}`} title="missing task" />
      ))}
    </div>
  )
}
