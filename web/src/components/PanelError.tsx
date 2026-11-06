import { Icon } from './Icon'
import styles from './PanelError.module.css'

export interface PanelErrorProps {
  /** One sentence; raw AWS text belongs in `detail`. */
  message: string
  detail?: string
  onRetry?: () => void
}

/** A panel's own data problem: the page around it stays usable. */
export function PanelError({ message, detail, onRetry }: PanelErrorProps) {
  return (
    <div role="alert" className={styles.error}>
      <Icon name="warn" />
      <div className={styles.text}>
        <p>{message}</p>
        {detail !== undefined && (
          <details className={styles.detail}>
            <summary>Show error</summary>
            <pre>{detail}</pre>
          </details>
        )}
      </div>
      {onRetry && (
        <button type="button" className={styles.retry} onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  )
}
