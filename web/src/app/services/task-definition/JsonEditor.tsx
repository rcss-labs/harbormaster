import styles from './taskDefinition.module.css'

interface JsonEditorProps {
  text: string
  error: string | null
  dirty: boolean
  readOnly: boolean
  /** True when the draft cannot be registered for a reason other than the JSON syntax (for example a bad image tag). */
  blocked?: boolean
  onChange(text: string): void
  onDiscard(): void
  onReview(): void
}

export function JsonEditor({ text, error, dirty, readOnly, blocked = false, onChange, onDiscard, onReview }: JsonEditorProps) {
  const status = error ?? (dirty ? 'Unsaved changes' : 'No changes')
  return (
    <>
      <textarea
        className={`${styles.editor} ${error ? styles.invalid : ''}`.trim()}
        spellCheck={false}
        readOnly={readOnly}
        aria-label="Task definition JSON"
        value={text}
        onChange={(event) => onChange(event.target.value)}
      />
      <div className={styles.toolbar}>
        <span className={error ? styles.err : styles.hint}>{status}</span>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} disabled={!dirty} onClick={onDiscard}>
          Discard
        </button>
        <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!dirty || error !== null || readOnly || blocked} onClick={onReview}>
          Review and save
        </button>
      </div>
    </>
  )
}
