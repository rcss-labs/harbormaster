import styles from './Meter.module.css'

export function Meter({ pct, color }: { pct: number; color: string }) {
  const clamped = Math.min(100, Math.max(0, pct))
  return (
    <div className={styles.meter}>
      <div className={styles.track}>
        <span style={{ width: `${clamped}%`, background: color }} />
      </div>
      <b>{Math.round(pct)}%</b>
    </div>
  )
}
