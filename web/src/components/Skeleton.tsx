import styles from './Skeleton.module.css'

interface SkeletonProps {
  lines?: number
  /** Height of each line in px. */
  height?: number
  /** Accessible name; defaults to "Loading". */
  label?: string
}

/** Placeholder bars shown while a panel's data loads. */
export function Skeleton({ lines = 3, height = 12, label = 'Loading' }: SkeletonProps) {
  return (
    <div role="status" aria-label={label} className={styles.skeleton}>
      {Array.from({ length: Math.max(1, lines) }, (_, index) => (
        <span key={index} className={styles.line} style={{ height, width: index === lines - 1 && lines > 1 ? '60%' : '100%' }} />
      ))}
    </div>
  )
}
