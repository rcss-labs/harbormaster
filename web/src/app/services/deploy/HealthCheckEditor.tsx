import { useState } from 'react'
import { Seg } from '../../../components/Seg'
import { CommandEditor } from './CommandEditor'
import { HEALTH_CHECK_DEFAULTS, HEALTH_CHECK_RANGES, type HealthCheck } from './draft'
import styles from './containers.module.css'
import form from './editors.module.css'

interface HealthCheckEditorProps {
  check: HealthCheck | null
  onChange: (check: HealthCheck | null) => void
}

type Kind = 'CMD-SHELL' | 'CMD'
type NumericField = keyof typeof HEALTH_CHECK_RANGES

const FIELD_LABELS: Record<NumericField, string> = {
  interval: 'Interval (s)',
  timeout: 'Timeout (s)',
  retries: 'Retries',
  startPeriod: 'Start period (s)',
}

const NEW_CHECK: HealthCheck = { command: ['CMD-SHELL', 'curl -f http://localhost/ || exit 1'], ...HEALTH_CHECK_DEFAULTS }

/** A number that is typed freely and clamped into its range when the field loses focus. */
function RangeField({ field, value, onCommit }: { field: NumericField; value: number; onCommit: (value: number) => void }) {
  const { min, max } = HEALTH_CHECK_RANGES[field]
  const [text, setText] = useState(String(value))
  // Follow the draft when it changes from outside (another edit, a reset); not on every keystroke.
  const [seen, setSeen] = useState(value)
  if (seen !== value) {
    setSeen(value)
    setText(String(value))
  }
  const commit = () => {
    const parsed = Number(text)
    const next = text.trim() === '' || Number.isNaN(parsed) ? HEALTH_CHECK_DEFAULTS[field] : Math.min(max, Math.max(min, Math.round(parsed)))
    setText(String(next))
    if (next !== value) onCommit(next)
  }
  return (
    <label className={form.field}>
      {FIELD_LABELS[field]}
      <input className={form.input} type="number" min={min} max={max} aria-label={FIELD_LABELS[field]} value={text} onChange={(event) => setText(event.target.value)} onBlur={commit} />
      <span className={form.hint}>{`${min}–${max}`}</span>
    </label>
  )
}

/** ECS container health check: a CMD-SHELL string or a CMD argument list, with the four timing fields. */
export function HealthCheckEditor({ check, onChange }: HealthCheckEditorProps) {
  if (!check) {
    return (
      <div className={form.actions}>
        <span className={form.hint}>No container health check.</span>
        <button type="button" className={form.btn} onClick={() => onChange(NEW_CHECK)}>
          Add health check
        </button>
      </div>
    )
  }

  const kind: Kind = check.command[0] === 'CMD' ? 'CMD' : 'CMD-SHELL'
  const rest = check.command.slice(1)
  const setCommand = (command: string[]) => onChange({ ...check, command })
  const switchKind = (next: Kind) => {
    if (next === kind) return
    setCommand(next === 'CMD' ? ['CMD', '/bin/sh', '-c', rest.join(' ')] : ['CMD-SHELL', rest.join(' ')])
  }

  return (
    <div className={form.stack}>
      <Seg<Kind>
        label="Health check type"
        options={[
          { value: 'CMD-SHELL', label: 'CMD-SHELL' },
          { value: 'CMD', label: 'CMD' },
        ]}
        value={kind}
        onChange={switchKind}
      />
      {kind === 'CMD-SHELL' ? (
        <label className={form.field}>
          Command
          <input
            className={`${form.input} ${form.mono}`}
            aria-label="Health check command"
            spellCheck={false}
            value={rest[0] ?? ''}
            onChange={(event) => setCommand(['CMD-SHELL', event.target.value])}
          />
        </label>
      ) : (
        <CommandEditor label="Health check" argv={rest} emptyHint="Add the executable and its arguments." onChange={(argv) => setCommand(['CMD', ...argv])} />
      )}
      <div className={styles.healthGrid}>
        {(Object.keys(HEALTH_CHECK_RANGES) as NumericField[]).map((field) => (
          <RangeField key={field} field={field} value={check[field]} onCommit={(value) => onChange({ ...check, [field]: value })} />
        ))}
      </div>
      <div className={form.actions}>
        <button type="button" className={`${form.btn} ${form.danger}`} onClick={() => onChange(null)}>
          Remove health check
        </button>
      </div>
    </div>
  )
}
