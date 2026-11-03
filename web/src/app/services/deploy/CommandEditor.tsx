import styles from './containers.module.css'
import form from './editors.module.css'

interface CommandEditorProps {
  /** "Command" or "Entry point": names the rows for assistive technology and the empty state. */
  label: string
  argv: string[]
  onChange: (argv: string[]) => void
  emptyHint?: string
}

/** One argument per row, as ECS stores them. An empty list means the image default (the key is removed upstream). */
export function CommandEditor({ label, argv, onChange, emptyHint = 'Uses the image default.' }: CommandEditorProps) {
  return (
    <div className={styles.rows}>
      {argv.length === 0 && <p className={form.hint}>{emptyHint}</p>}
      {argv.map((argument, position) => (
        <div className={styles.argvRow} key={position}>
          <input
            className={`${form.input} ${form.mono} ${argument === '' ? styles.invalid : ''}`.trim()}
            aria-label={`${label} argument ${position + 1}`}
            aria-invalid={argument === '' || undefined}
            spellCheck={false}
            value={argument}
            onChange={(event) => onChange(argv.map((current, at) => (at === position ? event.target.value : current)))}
          />
          <button type="button" className={styles.iconBtn} aria-label={`Remove ${label} argument ${position + 1}`} onClick={() => onChange(argv.filter((_, at) => at !== position))}>
            ×
          </button>
        </div>
      ))}
      {argv.includes('') && <p className={form.error}>Arguments cannot be empty.</p>}
      <div className={form.actions}>
        <button type="button" className={form.btn} aria-label={`Add ${label} argument`} onClick={() => onChange([...argv, ''])}>
          Add argument
        </button>
      </div>
    </div>
  )
}
