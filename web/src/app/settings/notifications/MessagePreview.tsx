import type { NotificationPreview } from '../../../api/notifications'
import { pendingSentence, previewLineItems, previewSentence } from './notifications'
import styles from './notifications.module.css'

interface MessagePreviewProps {
  preview: NotificationPreview | null
  error: string | null
  settleSeconds: number
}

/** What the route would send right now, and the message as the destination will show it. The server renders both. */
export function MessagePreview({ preview, error, settleSeconds }: MessagePreviewProps) {
  if (error) return <p className={styles.error}>{error}</p>
  if (!preview) return null
  const sentence = previewSentence(preview)
  const lines = previewLineItems(preview)
  return (
    <div className={styles.preview} data-preview="">
      <h3 className={styles.previewTitle}>Preview</h3>
      {preview.destination === 'webhook' ? (
        <pre className={styles.code} aria-label="Webhook body">
          {preview.json}
        </pre>
      ) : (
        <div className={styles.card} aria-label="Message">
          <span className={styles.cardTitle}>{preview.title}</span>
          <span className={styles.cardBody}>{preview.body}</span>
        </div>
      )}
      {preview.sample && <span className={`${styles.hint} ${styles.small}`}>Sample alert</span>}
      <p className={`${styles.sentence} ${sentence.warn ? styles.sentenceWarn : ''}`.trim()}>{sentence.text}</p>
      {lines.length > 0 && (
        <ul className={styles.lines} aria-label="What this route would send">
          {lines.map((line, index) => (
            <li key={`${index}-${line.text}`} className={line.muted ? styles.lineMuted : undefined}>
              {line.text}
            </li>
          ))}
        </ul>
      )}
      {preview.pending > 0 && settleSeconds > 0 && <p className={`${styles.help}`}>{pendingSentence(preview.pending, settleSeconds)}</p>}
    </div>
  )
}
