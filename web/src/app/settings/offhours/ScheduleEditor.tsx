import { useId, useMemo } from 'react'
import type { AwakeGrid, OffHoursOverlap, OffHoursPreview, OffHoursTagOption } from '../../../api/offhours'
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { PRESETS, presetGrid, stateKind, stateLabel } from './offhours'
import { WeekGrid } from './WeekGrid'
import styles from './offhours.module.css'

/** What the form edits; the tag is typed as `key=value`. */
export interface ScheduleDraft {
  name: string
  enabled: boolean
  tag: string
  timezone: string
  awake: AwakeGrid
}

interface ScheduleEditorProps {
  draft: ScheduleDraft
  onChange: (patch: Partial<ScheduleDraft>) => void
  isNew: boolean
  tags: OffHoursTagOption[]
  /** The preview of the current edits; null before the first answer. */
  preview: OffHoursPreview | null
  previewError: string | null
  /** The server clock, for state labels in the preview. */
  now: string
  currentCell: { day: number; hour: number } | null
  saving: boolean
  /** The server's sentence for a refused save (overlap, stale version, validation). */
  saveError: string | null
  onSave: () => void
  onDiscard: () => void
  onDelete?: () => void
}

let zoneCache: string[] | null = null
function supportedZones(): string[] {
  if (zoneCache) return zoneCache
  const supported = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  zoneCache = supported.includes('UTC') ? supported : ['UTC', ...supported]
  return zoneCache
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many)

function overlapSentence(overlap: OffHoursOverlap): string {
  return `${overlap.services.join(', ')} already ${plural(overlap.services.length, 'follows', 'follow')} "${overlap.schedule_name}".`
}

function Preview({ preview, error, now, timezone }: { preview: OffHoursPreview | null; error: string | null; now: string; timezone: string }) {
  if (error) return <p className={styles.error}>{error}</p>
  if (!preview) return null
  const production = preview.services.filter((service) => service.looks_like_production.length > 0)
  return (
    <div className={styles.preview} data-preview="">
      <h3 className={styles.previewTitle}>
        {`Before you save: ${preview.services.length} ${plural(preview.services.length, 'service matches', 'services match')} this tag`}
      </h3>
      {preview.services.length > 0 && (
        <ul className={styles.matched}>
          {preview.services.map((service) => (
            <li key={service.service_id}>
              {`${service.cluster}/${service.service} `}
              <Pill kind={stateKind(service.state)}>{stateLabel(service, now, timezone)}</Pill>
            </li>
          ))}
        </ul>
      )}
      {production.length > 0 && (
        <Banner kind="warn">
          <strong>{`${production.length} matched ${plural(production.length, 'service looks', 'services look')} like production:`}</strong>
          <ul className={styles.warnList}>
            {production.map((service) => (
              <li key={service.service_id}>
                <span className={styles.mono}>{`${service.cluster}/${service.service}`}</span>
                {' — '}
                <span>{service.looks_like_production.join('; ')}</span>
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {preview.overlaps.map((overlap) => (
        <Banner key={overlap.schedule_id} kind="warn">
          {overlapSentence(overlap)}
        </Banner>
      ))}
      {preview.sleep_now > 0 && (
        <Banner kind="info">{`${preview.sleep_now} ${plural(preview.sleep_now, 'service', 'services')} will go to sleep when you save.`}</Banner>
      )}
    </div>
  )
}

export function ScheduleEditor({
  draft,
  onChange,
  isNew,
  tags,
  preview,
  previewError,
  now,
  currentCell,
  saving,
  saveError,
  onSave,
  onDiscard,
  onDelete,
}: ScheduleEditorProps) {
  const tagListId = useId()
  const zones = useMemo(() => (supportedZones().includes(draft.timezone) ? supportedZones() : [draft.timezone, ...supportedZones()]), [draft.timezone])
  return (
    <Panel
      title={isNew ? 'New schedule' : draft.name || 'Schedule'}
      actions={
        <label className={styles.check}>
          <input type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ enabled: event.target.checked })} />
          Enabled
        </label>
      }
    >
      <div className={styles.editor}>
        <div className={styles.fields}>
          <label className={styles.field}>
            <span>Name</span>
            <input className={styles.ctl} value={draft.name} onChange={(event) => onChange({ name: event.target.value })} />
          </label>
          <label className={styles.field}>
            <span>Services tagged</span>
            <input
              className={styles.ctl}
              list={tagListId}
              role="combobox"
              placeholder="key=value"
              autoComplete="off"
              spellCheck={false}
              value={draft.tag}
              onChange={(event) => onChange({ tag: event.target.value })}
            />
          </label>
          <datalist id={tagListId}>
            {tags.map((option) => (
              <option key={`${option.key}=${option.value}`} value={`${option.key}=${option.value}`}>
                {`${option.services} ${plural(option.services, 'service', 'services')}`}
              </option>
            ))}
          </datalist>
          <label className={styles.field}>
            <span>Timezone</span>
            <select className={styles.ctl} value={draft.timezone} onChange={(event) => onChange({ timezone: event.target.value })}>
              {zones.map((zone) => (
                <option key={zone} value={zone}>
                  {zone}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className={styles.noteRow}>
          <span className={`${styles.muted} ${styles.small} ${styles.note}`}>
            {`Blue hours are awake. Times in ${draft.timezone}. Click or tab to a cell to toggle it.`}
          </span>
          <div className={styles.presets}>
            {PRESETS.map((name) => (
              <button key={name} type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => onChange({ awake: presetGrid(name) })}>
                {name}
              </button>
            ))}
          </div>
        </div>
        <div className={styles.gridScroll}>
          <WeekGrid value={draft.awake} onChange={(awake) => onChange({ awake })} now={currentCell} />
        </div>
        <p className={`${styles.muted} ${styles.small}`} style={{ margin: 0 }}>
          At sleep, Harbormaster sets the scalable target&apos;s min and max to 0 with a scheduled action, and restores the saved values at wake. Auto scaling
          policies stay in place.
        </p>
        <Preview preview={preview} error={previewError} now={now} timezone={draft.timezone} />
        {saveError && (
          <p role="alert" className={styles.error}>
            {saveError}
          </p>
        )}
        <div className={styles.actionsRow}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={saving} onClick={onSave}>
            Save
          </button>
          <button type="button" className={styles.btn} onClick={onDiscard}>
            Discard
          </button>
          {onDelete && (
            <button type="button" className={`${styles.btn} ${styles.danger} ${styles.spacer}`} onClick={onDelete}>
              Delete
            </button>
          )}
        </div>
      </div>
    </Panel>
  )
}
