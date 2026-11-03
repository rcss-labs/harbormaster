import { envProblems, type EnvEntry } from './draft'
import styles from './containers.module.css'
import form from './editors.module.css'

interface EnvEditorProps {
  entries: EnvEntry[]
  onChange: (entries: EnvEntry[]) => void
}

/** Plain environment variables (name and value). Anything sensitive belongs in Secrets, not here. */
export function EnvEditor({ entries, onChange }: EnvEditorProps) {
  const problems = envProblems(entries)
  const update = (position: number, patch: Partial<EnvEntry>) => onChange(entries.map((entry, at) => (at === position ? { ...entry, ...patch } : entry)))
  return (
    <div className={styles.rows}>
      {entries.length === 0 && <p className={form.hint}>No environment variables.</p>}
      {entries.map((entry, position) => (
        <div className={styles.envRow} key={position}>
          <input className={`${form.input} ${form.mono}`} aria-label={`Variable name ${position + 1}`} spellCheck={false} value={entry.name} onChange={(event) => update(position, { name: event.target.value })} />
          <input className={`${form.input} ${form.mono}`} aria-label={`Variable value ${position + 1}`} spellCheck={false} value={entry.value} onChange={(event) => update(position, { value: event.target.value })} />
          <button type="button" className={styles.iconBtn} aria-label={`Remove variable ${position + 1}`} onClick={() => onChange(entries.filter((_, at) => at !== position))}>
            ×
          </button>
        </div>
      ))}
      {problems.map((problem) => (
        <p key={problem} className={form.error} role="alert">
          {problem}
        </p>
      ))}
      <div className={form.actions}>
        <button type="button" className={form.btn} onClick={() => onChange([...entries, { name: '', value: '' }])}>
          Add variable
        </button>
      </div>
    </div>
  )
}
