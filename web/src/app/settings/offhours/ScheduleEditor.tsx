import { useMemo } from 'react'
import type {
  AwakeGrid,
  OffHoursExcludedService,
  OffHoursOverlap,
  OffHoursPreview,
  OffHoursSelector,
  OffHoursSelectorInput,
  OffHoursTags,
} from '../../../api/offhours'
import { Banner } from '../../../components/Banner'
import { MultiSelect, type MultiSelectOption } from '../../../components/MultiSelect'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { PRESETS, parseAddableAwsTag, presetGrid, selectorFromKey, selectorKey, stateKind, stateLabel } from './offhours'
import { WeekGrid } from './WeekGrid'
import styles from './offhours.module.css'

/** What the form edits. */
export interface ScheduleDraft {
  name: string
  enabled: boolean
  tags: OffHoursSelectorInput[]
  excluded: string[]
  timezone: string
  awake: AwakeGrid
}

interface ScheduleEditorProps {
  draft: ScheduleDraft
  onChange: (patch: Partial<ScheduleDraft>) => void
  isNew: boolean
  /** The tags that can be picked; undefined while loading. */
  tagOptions: OffHoursTags | undefined
  /** The saved schedule's tags and exclusions: names for choices the lists no longer offer. */
  savedTags: OffHoursSelector[]
  savedExcluded: OffHoursExcludedService[]
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

const MAX_OVERLAP_NAMES = 10

/** The same sentence the server refuses a save with, so the preview and the 409 read alike. */
function overlapSentence(overlap: OffHoursOverlap): string {
  const { services } = overlap
  const names =
    services.length <= MAX_OVERLAP_NAMES
      ? services.join(', ')
      : `${services.slice(0, MAX_OVERLAP_NAMES).join(', ')} and ${services.length - MAX_OVERLAP_NAMES} more`
  return `Schedule "${overlap.schedule_name}" already matches ${services.length} of these services: ${names}.`
}

function Preview({ preview, error, now, timezone }: { preview: OffHoursPreview | null; error: string | null; now: string; timezone: string }) {
  if (error) return <p className={styles.error}>{error}</p>
  if (!preview) return null
  const production = preview.services.filter((service) => service.looks_like_production.length > 0)
  return (
    <div className={styles.preview} data-preview="">
      <h3 className={styles.previewTitle}>
        {`Before you save: ${preview.services.length} ${plural(preview.services.length, 'service matches', 'services match')} these tags${
          preview.excluded.length > 0 ? ` (${preview.excluded.length} excluded)` : ''
        }`}
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

const sectionAws = 'AWS tags'
const sectionHarbormaster = 'Harbormaster tags'

/** The pickable tags, plus any selected tag the lists no longer offer (so its chip still has a name). */
function tagChoices(tagOptions: OffHoursTags | undefined, selected: OffHoursSelectorInput[], saved: OffHoursSelector[]): MultiSelectOption[] {
  const count = (n: number) => `${n} ${plural(n, 'service', 'services')}`
  const options: MultiSelectOption[] = [
    ...(tagOptions?.harbormaster ?? []).map((tag) => ({
      value: selectorKey({ kind: 'harbormaster', tag_id: tag.id }),
      label: tag.name,
      swatch: tag.color,
      hint: count(tag.services),
      group: sectionHarbormaster,
    })),
    ...(tagOptions?.items ?? []).map((tag) => ({
      value: selectorKey({ kind: 'aws', key: tag.key, value: tag.value }),
      label: `${tag.key}=${tag.value}`,
      hint: count(tag.services),
      group: sectionAws,
    })),
  ]
  for (const tag of selected) {
    const value = selectorKey(tag)
    if (options.some((option) => option.value === value)) continue
    if (tag.kind === 'aws') {
      options.push({ value, label: `${tag.key}=${tag.value}`, group: sectionAws })
    } else {
      const known = saved.find((s) => s.kind === 'harbormaster' && s.tag_id === tag.tag_id)
      options.push({ value, label: known?.name ?? `Tag ${tag.tag_id}`, swatch: known?.color, group: sectionHarbormaster })
    }
  }
  return options
}

/** The services the tags match (kept and excluded), plus excluded ids the tags no longer match. */
function excludeChoices(preview: OffHoursPreview | null, excluded: string[], saved: OffHoursExcludedService[]): MultiSelectOption[] {
  const known = preview ? [...preview.services, ...preview.excluded] : []
  const options: MultiSelectOption[] = known.map((service) => ({
    value: service.service_id,
    label: `${service.cluster}/${service.service}`,
    hint: `${service.account_name} · ${service.region}`,
    keywords: `${service.service} ${service.cluster} ${service.account_name} ${service.region}`,
  }))
  for (const id of excluded) {
    if (known.some((service) => service.service_id === id)) continue
    const old = saved.find((service) => service.service_id === id)
    options.push({
      value: id,
      label: old ? `${old.cluster}/${old.service}` : id,
      // Without a preview answer yet, nothing can be said about whether the tags still match it.
      hint: preview ? 'no longer matched' : old ? `${old.account_name} · ${old.region}` : undefined,
      keywords: old ? `${old.service} ${old.cluster} ${old.account_name} ${old.region}` : id,
    })
  }
  return options
}

export function ScheduleEditor({
  draft,
  onChange,
  isNew,
  tagOptions,
  savedTags,
  savedExcluded,
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
  const tagPickerOptions = useMemo(() => tagChoices(tagOptions, draft.tags, savedTags), [tagOptions, draft.tags, savedTags])
  const excludeOptions = useMemo(() => excludeChoices(preview, draft.excluded, savedExcluded), [preview, draft.excluded, savedExcluded])
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
            <span>Timezone</span>
            <select className={styles.ctl} value={draft.timezone} onChange={(event) => onChange({ timezone: event.target.value })}>
              {zones.map((zone) => (
                <option key={zone} value={zone}>
                  {zone}
                </option>
              ))}
            </select>
          </label>
          <div className={`${styles.field} ${styles.wide}`}>
            <span>Services tagged</span>
            <MultiSelect
              label="Services tagged"
              placeholder="Search tags"
              options={tagPickerOptions}
              value={draft.tags.map(selectorKey)}
              searchable
              chips
              creatable={(query) => {
                const parsed = parseAddableAwsTag(query)
                return parsed ? { value: selectorKey({ kind: 'aws', ...parsed }), label: `${parsed.key}=${parsed.value}`, group: 'AWS tags' } : null
              }}
              onChange={(values) =>
                onChange({
                  tags: values.flatMap((value) => {
                    const kept = draft.tags.find((tag) => selectorKey(tag) === value)
                    const parsed = kept ?? selectorFromKey(value)
                    return parsed ? [parsed] : []
                  }),
                })
              }
            />
            <span className={`${styles.muted} ${styles.small}`}>Services with any of these tags.</span>
          </div>
          <div className={`${styles.field} ${styles.wide}`}>
            <span>Exclude services</span>
            <MultiSelect
              label="Exclude services"
              placeholder="Search services to leave out"
              options={excludeOptions}
              value={draft.excluded}
              searchable
              chips
              onChange={(excluded) => onChange({ excluded })}
            />
            <span className={`${styles.muted} ${styles.small}`}>These services are left alone even when a tag above matches them.</span>
          </div>
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
