import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import {
  useDeleteSavedLogSearch,
  useLogSearchServices,
  useRunLogSearch,
  useSaveLogSearch,
  useSavedLogSearches,
  type LogSearchRange,
  type LogSearchRequest,
  type SavedLogSearchDTO,
} from '../../api/logSearch'
import { useNow } from '../../components/Ago'
import { Banner } from '../../components/Banner'
import { BottomSheet } from '../../components/BottomSheet'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { Seg } from '../../components/Seg'
import { useToast } from '../../components/Toast'
import { usePhone } from '../../components/usePhone'
import { useCrumbs } from '../shell/crumbs'
import { HitsByService } from './HitsByService'
import { HitsHistogram } from './HitsHistogram'
import { formFromParams, paramsFromForm, RANGES, validateForm, type SearchForm } from './model'
import { ResultLines } from './ResultLines'
import { ResultsHeader } from './ResultsHeader'
import { SavedQueries } from './SavedQueries'
import { ServicePicker } from './ServicePicker'
import styles from './logsearch.module.css'

const MAX_NAME_CHARS = 80
const HOUR_MS = 60 * 60 * 1000
const QUERY_PLACEHOLDER = 'filter @message like /timeout/'
const QUERY_RULES_HINT =
  'Queries can use only fields, filter, parse, dedup, sort and limit. A quoted string cannot contain its own quote type; switch to the other quote type.'

const pad = (value: number) => String(value).padStart(2, '0')

/** RFC 3339 to the `datetime-local` value, in the browser's time zone. */
function toInputValue(iso: string | undefined): string {
  const date = new Date(iso ?? '')
  if (Number.isNaN(date.getTime())) return ''
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function fromInputValue(value: string): string | undefined {
  const date = new Date(value)
  return value === '' || Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

const requestFrom = (form: SearchForm): LogSearchRequest => ({
  service_ids: form.serviceIds,
  range: form.range,
  ...(form.range === 'custom' ? { from: form.from, to: form.to } : {}),
  query: form.query,
})

export function LogSearchPage() {
  useCrumbs([['Estate', '/'], ['Log search']])
  const phone = usePhone()
  const toast = useToast()
  const now = useNow(15_000)
  const services = useLogSearchServices()
  const saved = useSavedLogSearches()
  const run = useRunLogSearch()
  const save = useSaveLogSearch()
  const remove = useDeleteSavedLogSearch()

  // The form lives in state and is mirrored to the URL, so typing is never held back by a navigation. Opening a URL only fills the form.
  const [params, setParams] = useSearchParams()
  const [form, setForm] = useState<SearchForm>(() => formFromParams(params))
  const paramsRef = useRef(params)
  useEffect(() => {
    paramsRef.current = params
  })
  useEffect(() => {
    const next = paramsFromForm(form)
    if (next.toString() !== paramsRef.current.toString()) setParams(next, { replace: true })
  }, [form, setParams])

  const [savedOpen, setSavedOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveName, setSaveName] = useState('')
  const [saveError, setSaveError] = useState('')
  const [deleting, setDeleting] = useState<SavedLogSearchDTO | null>(null)
  const [hiddenServices, setHiddenServices] = useState(0)

  const invalid = validateForm(form, now)
  const running = run.isPending
  const failure = run.error && run.error.name !== 'AbortError' ? run.error : null
  const savedItems = saved.data?.items ?? []

  const setRange = (range: LogSearchRange) => {
    if (range !== 'custom') return setForm({ serviceIds: form.serviceIds, range, query: form.query })
    const end = Date.now()
    setForm({ ...form, range, from: new Date(end - HOUR_MS).toISOString(), to: new Date(end).toISOString() })
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (invalid === null && !running) run.mutate(requestFrom(form))
  }

  const load = (item: SavedLogSearchDTO) => {
    setForm({
      serviceIds: item.service_ids,
      range: item.range,
      ...(item.range === 'custom' ? { from: item.from ?? undefined, to: item.to ?? undefined } : {}),
      query: item.query,
    })
    setHiddenServices(item.hidden_services)
    setSavedOpen(false)
  }

  const openSave = () => {
    setSaveName('')
    setSaveError('')
    setSaving(true)
  }

  const trimmedName = saveName.trim()
  const replaced = savedItems.some((item) => item.name === trimmedName)
  const confirmSave = () => {
    if (trimmedName === '' || save.isPending) return
    setSaveError('')
    save.mutate(
      {
        name: trimmedName,
        query: form.query,
        service_ids: form.serviceIds,
        range: form.range,
        from: form.range === 'custom' ? (form.from ?? null) : null,
        to: form.range === 'custom' ? (form.to ?? null) : null,
      },
      {
        onSuccess: () => {
          setSaving(false)
          toast.show(`Saved "${trimmedName}".`, 'ok')
        },
        onError: (error) => setSaveError(error.message),
      },
    )
  }

  const confirmDelete = () => {
    if (!deleting) return
    remove.mutate(deleting.id, {
      onSuccess: () => setDeleting(null),
      onError: (error) => {
        setDeleting(null)
        toast.show(error.message, 'bad')
      },
    })
  }

  const head = <h1 className="hm-phone-title-sr">Log search</h1>

  if (services.isPending) return <>{head}<Panel title="Search" loading>{null}</Panel></>
  if (services.isError) return <>{head}<EmptyState>{services.error.message}</EmptyState></>
  if (services.data.items.length === 0) {
    return (
      <>
        {head}
        <EmptyState>No services are available to search.</EmptyState>
      </>
    )
  }

  const savedPanelBody = saved.isError ? <p className={styles.hint}>Saved queries could not be loaded.</p> : <SavedQueries items={savedItems} onLoad={load} onDelete={setDeleting} />

  return (
    <div className={styles.page}>
      {head}
      <div className={styles.top}>
        <Panel title="Search">
          <form className={styles.form} aria-label="Log search" data-layout={phone ? 'stacked' : 'wide'} onSubmit={submit}>
            <ServicePicker services={services.data.items} selectedIds={form.serviceIds} onChange={(serviceIds) => setForm({ ...form, serviceIds })} />
            <div className={styles.field}>
              <span className={styles.fieldLabel}>Range</span>
              <Seg label="Range" options={RANGES.map((range) => ({ value: range.value, label: range.label }))} value={form.range} onChange={setRange} />
            </div>
            {form.range === 'custom' && (
              <div className={styles.dates}>
                <label className={styles.field}>
                  <span className={styles.fieldLabel}>From</span>
                  <input className={styles.control} type="datetime-local" value={toInputValue(form.from)} onChange={(event) => setForm({ ...form, from: fromInputValue(event.target.value) })} />
                </label>
                <label className={styles.field}>
                  <span className={styles.fieldLabel}>To</span>
                  <input className={styles.control} type="datetime-local" value={toInputValue(form.to)} onChange={(event) => setForm({ ...form, to: fromInputValue(event.target.value) })} />
                </label>
              </div>
            )}
            <label className={styles.field}>
              <span className={styles.fieldLabel}>Query</span>
              <textarea
                className={`${styles.control} ${styles.query}`}
                spellCheck={false}
                placeholder={QUERY_PLACEHOLDER}
                value={form.query}
                onChange={(event) => setForm({ ...form, query: event.target.value })}
              />
            </label>
            <div className={styles.actions}>
              {running ? (
                <button type="button" className={styles.btn} onClick={run.cancel}>
                  Cancel
                </button>
              ) : (
                <button type="submit" className={`${styles.btn} ${styles.primary}`} disabled={invalid !== null} aria-describedby={invalid ? 'log-search-invalid' : undefined}>
                  Run query
                </button>
              )}
              <button type="button" className={styles.btn} disabled={invalid !== null || running} onClick={openSave}>
                Save this query
              </button>
              {phone && (
                <button type="button" className={styles.btn} aria-haspopup="dialog" onClick={() => setSavedOpen(true)}>
                  Saved
                </button>
              )}
            </div>
            {invalid && !running && (
              <p id="log-search-invalid" className={styles.invalid}>
                {invalid}
              </p>
            )}
            {running && (
              <p className={styles.hint} role="status">
                Running the query. Cancel it if it takes too long.
              </p>
            )}
          </form>
        </Panel>
        {!phone && (
          <Panel title="Saved queries" loading={saved.isPending}>
            {savedPanelBody}
          </Panel>
        )}
      </div>

      {hiddenServices > 0 && <Banner kind="info">{`${hiddenServices} services in this query are no longer available to you.`}</Banner>}
      {failure && (
        <div role="alert">
          <Banner kind="bad">
            {failure.message}
            {'status' in failure && failure.status === 422 && <span className={styles.hint}>{` ${QUERY_RULES_HINT}`}</span>}
          </Banner>
        </div>
      )}

      {run.data && (
        <div className={styles.page} key={run.submittedAt}>
          <ResultsHeader result={run.data} />
          {run.data.rows.length === 0 ? (
            <EmptyState>No lines matched.</EmptyState>
          ) : (
            <>
              <div className={styles.charts}>
                <Panel title="Hits over time" sub={run.data.capped ? <span className={styles.hint}>lines shown only</span> : undefined}>
                  <HitsHistogram result={run.data} />
                </Panel>
                <Panel title="Hits by service" footer="Select a line to open that service's Logs tab at that second.">
                  <HitsByService result={run.data} />
                </Panel>
              </div>
              <Panel title="Lines" sub={<span className={styles.hint}>Times are UTC.</span>} flush>
                <ResultLines result={run.data} />
              </Panel>
            </>
          )}
        </div>
      )}

      {phone && (
        <BottomSheet open={savedOpen} onClose={() => setSavedOpen(false)} label="Saved queries" title="Saved queries">
          {saved.isPending ? <p className={styles.hint}>Loading…</p> : savedPanelBody}
        </BottomSheet>
      )}

      {saving && (
        <Dialog
          title="Save this query"
          onClose={() => setSaving(false)}
          actions={[
            { label: 'Cancel', onClick: () => setSaving(false) },
            { label: 'Save', kind: 'primary', disabled: trimmedName === '' || save.isPending, onClick: confirmSave },
          ]}
        >
          <label className={styles.field}>
            <span className={styles.fieldLabel}>Name</span>
            <input
              className={styles.control}
              maxLength={MAX_NAME_CHARS}
              value={saveName}
              onChange={(event) => setSaveName(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault()
                  confirmSave()
                }
              }}
            />
          </label>
          {replaced && <p className={styles.replaceNote}>{`This replaces the saved query "${trimmedName}".`}</p>}
          {saveError && <p className={styles.dialogError}>{saveError}</p>}
        </Dialog>
      )}

      {deleting && (
        <ConfirmDialog
          title={`Delete "${deleting.name}"?`}
          confirmLabel="Delete"
          danger
          confirmDisabled={remove.isPending}
          onConfirm={confirmDelete}
          onCancel={() => setDeleting(null)}
        >
          This removes the saved query. Searches you already ran are not affected.
        </ConfirmDialog>
      )}
    </div>
  )
}
