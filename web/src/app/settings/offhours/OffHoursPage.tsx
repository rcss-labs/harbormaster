import { useEffect, useMemo, useState } from 'react'
import { ApiError } from '../../../api/client'
import {
  useCreateOffHoursSchedule,
  useDeleteOffHoursSchedule,
  useOffHoursPreview,
  useOffHoursSchedule,
  useOffHoursSchedules,
  useOffHoursTags,
  useUpdateOffHoursSchedule,
  type OffHoursPreviewRequest,
  type OffHoursSchedule,
  type OffHoursScheduleInput,
} from '../../../api/offhours'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { KpiStrip, type KpiItem } from '../../../components/KpiStrip'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { useToast } from '../../../components/Toast'
import { money } from '../../analytics/cost'
import { HeaderAction } from '../HeaderAction'
import { NOT_PRICED, awakeHours, browserTimezone, currentCell, emptyGrid, parseTagSelector } from './offhours'
import { ScheduleEditor, type ScheduleDraft } from './ScheduleEditor'
import { ScheduleServices } from './ScheduleServices'
import styles from './offhours.module.css'

const PREVIEW_DEBOUNCE_MS = 400

type Selection = number | 'new'

interface DraftState {
  key: string
  version: number | undefined
  baseline: ScheduleDraft
  draft: ScheduleDraft
}

const draftOf = (schedule: OffHoursSchedule): ScheduleDraft => ({
  name: schedule.name,
  enabled: schedule.enabled,
  tag: `${schedule.tag_key}=${schedule.tag_value}`,
  timezone: schedule.timezone,
  awake: schedule.awake,
})

const blankDraft = (): ScheduleDraft => ({ name: '', enabled: true, tag: '', timezone: browserTimezone(), awake: emptyGrid() })

const dollars = (value: number | null) =>
  value === null ? (
    <span title={NOT_PRICED}>—</span>
  ) : (
    money(value)
  )

function messageOf(error: unknown): string {
  return error instanceof ApiError ? error.message : 'Something went wrong. Try again.'
}

export function OffHoursPage() {
  const toast = useToast()
  const list = useOffHoursSchedules()
  const tags = useOffHoursTags()
  const [picked, setPicked] = useState<Selection | null>(null)
  const items = useMemo(() => list.data?.items ?? [], [list.data])
  // Until someone picks a row, the first schedule is open; with none at all, the editor starts a new one.
  const selected: Selection = picked ?? items[0]?.id ?? 'new'
  const detail = useOffHoursSchedule(selected === 'new' ? null : selected)
  const detailData = detail.data?.id === selected ? detail.data : undefined
  // null starts a blank schedule; undefined means the saved one has not loaded yet.
  const source: OffHoursSchedule | null | undefined = selected === 'new' ? null : detailData

  // The form starts from the saved schedule whenever another one is opened. Later refetches (a wake, another admin)
  // do not overwrite edits; a save the server refuses as stale says so.
  const [state, setState] = useState<DraftState | null>(null)
  const key = String(selected)
  if (!list.isPending && state?.key !== key && source !== undefined) {
    const baseline = source ? draftOf(source) : blankDraft()
    setState({ key, version: source?.version, baseline, draft: baseline })
  }
  const draft = state?.key === key ? state.draft : null
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  const create = useCreateOffHoursSchedule()
  const update = useUpdateOffHoursSchedule(selected === 'new' ? 0 : selected)
  const remove = useDeleteOffHoursSchedule()

  const scheduleId = selected === 'new' ? undefined : selected
  const tagText = draft?.tag
  const selector = useMemo(() => (tagText === undefined ? null : parseTagSelector(tagText)), [tagText])
  const previewBody = useMemo<OffHoursPreviewRequest | null>(
    () =>
      draft && selector
        ? { name: draft.name, enabled: draft.enabled, tag_key: selector.key, tag_value: selector.value, timezone: draft.timezone, awake: draft.awake, schedule_id: scheduleId }
        : null,
    [draft, selector, scheduleId],
  )
  const preview = useOffHoursPreview()
  const { mutate: requestPreview } = preview
  useEffect(() => {
    if (!previewBody) return
    const timer = setTimeout(() => requestPreview(previewBody), PREVIEW_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [previewBody, requestPreview])
  // An answer for another schedule must not feed this one's numbers.
  const previewFresh = preview.variables?.schedule_id === scheduleId
  const previewData = previewFresh ? (preview.data ?? null) : null
  const previewError = previewFresh && preview.error ? messageOf(preview.error) : null

  const patchDraft = (patch: Partial<ScheduleDraft>) => {
    setSaveError(null)
    setState((current) => (current ? { ...current, draft: { ...current.draft, ...patch } } : current))
  }

  const save = () => {
    if (!state) return
    const parsed = parseTagSelector(state.draft.tag)
    if (!parsed) {
      setSaveError('Enter the tag as key=value, for example env=staging.')
      return
    }
    setSaveError(null)
    const body: OffHoursScheduleInput = {
      name: state.draft.name,
      enabled: state.draft.enabled,
      tag_key: parsed.key,
      tag_value: parsed.value,
      timezone: state.draft.timezone,
      awake: state.draft.awake,
      ...(selected === 'new' ? {} : { version: state.version }),
    }
    const mutation = selected === 'new' ? create : update
    mutation.mutate(body, {
      onSuccess: (saved) => {
        toast.show('Saved.', 'ok')
        const savedDraft = draftOf(saved)
        setPicked(saved.id)
        setState({ key: String(saved.id), version: saved.version, baseline: savedDraft, draft: savedDraft })
      },
      // The server's sentence is authoritative (overlap, stale version, validation); the edits stay in the form.
      onError: (error) => setSaveError(messageOf(error)),
    })
  }

  const removeSchedule = () => {
    if (selected === 'new') return
    remove.mutate(selected, {
      onSuccess: () => {
        setConfirmingDelete(false)
        toast.show('Schedule deleted.', 'ok')
        setPicked(null)
        setState(null)
      },
      onError: (error) => {
        setConfirmingDelete(false)
        setSaveError(messageOf(error))
      },
    })
  }

  const open = (next: Selection) => {
    setSaveError(null)
    setPicked(next)
  }

  const now = list.data?.now ?? new Date().toISOString()
  const savedSaving = detailData ? detailData.monthly_saving : null
  const kpis: KpiItem[] = [
    { label: 'Estimated saving a month, all schedules', value: dollars(list.data?.monthly_saving_total ?? null) },
    { label: 'Awake a week on this schedule (of 168)', value: draft ? `${awakeHours(draft.awake)} h` : '—' },
    { label: 'Saving a month on this schedule', value: dollars(previewData ? previewData.monthly_saving : savedSaving) },
  ]

  return (
    <div className={styles.page}>
      <HeaderAction>
        <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => open('new')}>
          Add schedule
        </button>
      </HeaderAction>
      <div className={styles.head}>
        <h2>Off-hours schedules</h2>
      </div>
      <KpiStrip label="Off-hours savings" items={kpis} />
      <div className={styles.split}>
        <Panel
          title="Schedules"
          flush
          loading={list.isPending}
          error={list.isError ? { message: 'Could not load the off-hours schedules.', detail: messageOf(list.error), onRetry: () => void list.refetch() } : undefined}
        >
          {items.length === 0 ? (
            <p className={styles.listEmpty}>No schedules yet. Add one to put tagged services to sleep out of hours.</p>
          ) : (
            <ul className={styles.list} aria-label="Schedules">
              {items.map((schedule) => (
                <li key={schedule.id} className={styles.listItem}>
                  <button type="button" className={styles.listButton} aria-current={selected === schedule.id} onClick={() => open(schedule.id)}>
                    <span>
                      <span className={styles.listName}>{schedule.name}</span>
                      <span className={`${styles.muted} ${styles.small}`}>{`${schedule.tag_key}=${schedule.tag_value} · ${schedule.awake_hours_per_week} h awake a week`}</span>
                    </span>
                    <Pill kind={schedule.enabled ? 'ok' : 'mute'}>{schedule.enabled ? 'On' : 'Off'}</Pill>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>
        {draft ? (
          <ScheduleEditor
            draft={draft}
            onChange={patchDraft}
            isNew={selected === 'new'}
            tags={tags.data?.items ?? []}
            preview={previewData}
            previewError={previewError}
            now={now}
            currentCell={currentCell(now, draft.timezone)}
            saving={create.isPending || update.isPending}
            saveError={saveError}
            onSave={save}
            onDiscard={() => {
              setSaveError(null)
              setState((current) => (current ? { ...current, draft: current.baseline } : current))
            }}
            onDelete={selected === 'new' ? undefined : () => setConfirmingDelete(true)}
          />
        ) : (
          <Panel title="Schedule" loading={detail.isPending || list.isPending} error={detail.isError ? { message: 'Could not load this schedule.', detail: messageOf(detail.error) } : undefined}>
            {null}
          </Panel>
        )}
      </div>
      {detailData && <ScheduleServices services={detailData.services} timezone={detailData.timezone} now={now} />}
      {confirmingDelete && (
        <ConfirmDialog
          title={`Delete "${draft?.name ?? ''}"?`}
          confirmLabel="Delete schedule"
          danger
          confirmDisabled={remove.isPending}
          onConfirm={removeSchedule}
          onCancel={() => setConfirmingDelete(false)}
        >
          Services on this schedule get their saved capacity back.
        </ConfirmDialog>
      )}
    </div>
  )
}
