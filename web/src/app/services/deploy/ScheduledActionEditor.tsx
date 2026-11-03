import { useState } from 'react'
import { Seg } from '../../../components/Seg'
import type { ScheduledActionDraft } from './scalingDraft'
import styles from './editors.module.css'

type Kind = 'cron' | 'at' | 'rate'
type RateUnit = 'minutes' | 'hours' | 'days'

interface ScheduledActionEditorProps {
  /** The action being edited, or null for a new one. */
  initial: ScheduledActionDraft | null
  /** Names already taken by other actions; the edited action's own name is allowed. */
  existingNames: string[]
  onSave: (action: ScheduledActionDraft) => void
  onCancel: () => void
}

const NAME_PATTERN = /^[\w./-]{1,256}$/
const AT_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/
const DEFAULT_CRON = '0 7 * * ? *'

function timezones(): string[] {
  const supported = (Intl as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf
  return supported ? Array.from(new Set(['UTC', ...supported('timeZone')])) : []
}

function parseInitial(schedule: string): { kind: Kind; cron: string; at: string; every: string; unit: RateUnit } {
  const base = { kind: 'cron' as Kind, cron: DEFAULT_CRON, at: '', every: '1', unit: 'hours' as RateUnit }
  const cron = /^cron\((.*)\)$/.exec(schedule)
  if (cron) return { ...base, cron: cron[1] }
  const at = /^at\((.*)\)$/.exec(schedule)
  if (at) return { ...base, kind: 'at', at: at[1] }
  const rate = /^rate\((\d+) (minute|hour|day)s?\)$/.exec(schedule)
  if (rate) return { ...base, kind: 'rate', every: rate[1], unit: `${rate[2]}s` as RateUnit }
  return base
}

const parseCapacity = (text: string): number | null => (text.trim() === '' ? null : Number(text))
const isCapacity = (value: number | null) => value === null || (Number.isInteger(value) && value >= 0)

/** Inline form for one scheduled scaling action: cron / at / rate schedule, timezone and min/max capacity. */
export function ScheduledActionEditor({ initial, existingNames, onSave, onCancel }: ScheduledActionEditorProps) {
  const parsed = parseInitial(initial?.schedule ?? '')
  const [name, setName] = useState(initial?.name ?? '')
  const [kind, setKind] = useState<Kind>(parsed.kind)
  const [cron, setCron] = useState(parsed.cron)
  const [at, setAt] = useState(parsed.at)
  const [every, setEvery] = useState(parsed.every)
  const [unit, setUnit] = useState<RateUnit>(parsed.unit)
  const [timezone, setTimezone] = useState(initial?.timezone || 'UTC')
  const [minimum, setMinimum] = useState(initial?.min_capacity == null ? '' : String(initial.min_capacity))
  const [maximum, setMaximum] = useState(initial?.max_capacity == null ? '' : String(initial.max_capacity))
  const zones = timezones()
  const zoneOptions = zones.includes(timezone) ? zones : [timezone, ...zones]

  const everyNumber = Number(every)
  let schedule: string | null = null
  if (kind === 'cron' && cron.trim().split(/\s+/).length === 6) schedule = `cron(${cron.trim().replace(/\s+/g, ' ')})`
  if (kind === 'at' && AT_PATTERN.test(at)) schedule = `at(${at.length === 16 ? `${at}:00` : at})`
  if (kind === 'rate' && Number.isInteger(everyNumber) && everyNumber >= 1) {
    schedule = `rate(${everyNumber} ${everyNumber === 1 ? unit.slice(0, -1) : unit})`
  }

  const min = parseCapacity(minimum)
  const max = parseCapacity(maximum)
  const capacityOk = (min !== null || max !== null) && isCapacity(min) && isCapacity(max) && (min === null || max === null || min <= max)
  const nameTaken = existingNames.includes(name) && name !== initial?.name
  const nameOk = NAME_PATTERN.test(name) && !nameTaken
  const valid = nameOk && schedule !== null && capacityOk && timezone.trim() !== ''

  return (
    <form
      className={styles.editor}
      aria-label={initial ? `Edit ${initial.name}` : 'New scheduled action'}
      onSubmit={(event) => {
        event.preventDefault()
        if (valid && schedule) onSave({ name, schedule, timezone: timezone.trim(), min_capacity: min, max_capacity: max })
      }}
    >
      <div className={styles.fieldRow}>
        <label className={styles.field}>
          Name
          <input className={styles.input} value={name} disabled={initial !== null} onChange={(event) => setName(event.target.value)} />
        </label>
        <label className={styles.field}>
          Timezone
          {zones.length > 0 ? (
            <select className={styles.select} value={timezone} onChange={(event) => setTimezone(event.target.value)}>
              {zoneOptions.map((zone) => (
                <option key={zone} value={zone}>
                  {zone}
                </option>
              ))}
            </select>
          ) : (
            <input className={styles.input} value={timezone} placeholder="Region/City" onChange={(event) => setTimezone(event.target.value)} />
          )}
        </label>
      </div>
      {nameTaken && <p className={styles.error}>A scheduled action named {name} already exists.</p>}

      <Seg
        label="Schedule type"
        value={kind}
        onChange={setKind}
        options={[
          { value: 'cron', label: 'Cron' },
          { value: 'at', label: 'At' },
          { value: 'rate', label: 'Rate' },
        ]}
      />
      {kind === 'cron' && (
        <div className={styles.field}>
          <label className={styles.field}>
            Cron fields
            <input className={`${styles.input} ${styles.mono}`} value={cron} onChange={(event) => setCron(event.target.value)} />
          </label>
          <span className={styles.hint}>Minutes, hours, day of month, month, day of week, year: for example 0 7 * * ? *</span>
        </div>
      )}
      {kind === 'at' && (
        <label className={styles.field}>
          Date and time
          <input className={styles.input} type="datetime-local" step={1} value={at} onChange={(event) => setAt(event.target.value)} />
        </label>
      )}
      {kind === 'rate' && (
        <div className={styles.fieldRow}>
          <label className={styles.field}>
            Every
            <input className={styles.input} type="number" min={1} value={every} onChange={(event) => setEvery(event.target.value)} />
          </label>
          <label className={styles.field}>
            Unit
            <select className={styles.select} value={unit} onChange={(event) => setUnit(event.target.value as RateUnit)}>
              <option value="minutes">minutes</option>
              <option value="hours">hours</option>
              <option value="days">days</option>
            </select>
          </label>
        </div>
      )}

      <div className={styles.fieldRow}>
        <label className={styles.field}>
          Minimum capacity
          <input className={styles.input} type="number" min={0} value={minimum} onChange={(event) => setMinimum(event.target.value)} />
        </label>
        <label className={styles.field}>
          Maximum capacity
          <input className={styles.input} type="number" min={0} value={maximum} onChange={(event) => setMaximum(event.target.value)} />
        </label>
      </div>
      <p className={styles.hint}>Set at least one. The other stays as it is when the action runs.</p>

      <div className={styles.actions}>
        <button type="submit" className={`${styles.btn} ${styles.primary}`} disabled={!valid}>
          Save action
        </button>
        <button type="button" className={styles.btn} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}
