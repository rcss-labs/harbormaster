import { useMemo, useRef } from 'react'
import { changedLineNumbers } from './diffLines'
import styles from './taskDefinition.module.css'

interface JsonEditorProps {
  text: string
  /** The deployed definition's JSON; lines that differ from it are marked in the gutter. */
  initialText: string
  /** The revision number a save would register, shown on the review button. */
  registerRevision: number
  error: string | null
  dirty: boolean
  readOnly: boolean
  /** True when the draft cannot be registered for a reason other than the JSON syntax (for example a bad image tag). */
  blocked?: boolean
  onChange(text: string): void
  onDiscard(): void
  onReview(): void
}

export function JsonEditor({ text, initialText, registerRevision, error, dirty, readOnly, blocked = false, onChange, onDiscard, onReview }: JsonEditorProps) {
  const status = error ?? (dirty ? 'Unsaved changes' : 'No changes')
  const gutterRef = useRef<HTMLDivElement>(null)
  const lineCount = text.split('\n').length
  const changed = useMemo(() => changedLineNumbers(initialText, text.replace(/\s+$/, '')), [initialText, text])
  return (
    <>
      <div className={`${styles.editorWrap} ${error ? styles.invalidWrap : ''}`.trim()}>
        <div className={styles.gutter} ref={gutterRef} data-testid="json-gutter" aria-hidden="true">
          {Array.from({ length: lineCount }, (_, index) => (
            <div key={index} data-changed={changed.has(index + 1) ? '' : undefined} />
          ))}
        </div>
        <textarea
          className={styles.editor}
          spellCheck={false}
          wrap="off"
          readOnly={readOnly}
          aria-label="Task definition JSON"
          value={text}
          onChange={(event) => onChange(event.target.value)}
          onScroll={(event) => {
            if (gutterRef.current) gutterRef.current.scrollTop = event.currentTarget.scrollTop
          }}
        />
      </div>
      <div className={styles.toolbar}>
        <span className={error ? styles.err : styles.hint}>{status}</span>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} disabled={!dirty} onClick={onDiscard}>
          Discard
        </button>
        <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!dirty || error !== null || readOnly || blocked} onClick={onReview}>
          {`Review and register :${registerRevision}`}
        </button>
      </div>
    </>
  )
}
