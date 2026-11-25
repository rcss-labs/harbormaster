import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useAgentReviews, useCreateReview, useReviewPreview, useUpdateReview, type AgentReviewsInfo, type ReviewCadence, type ReviewDTO } from '../../../api/agent'
import { ApiError } from '../../../api/client'
import { useNotificationRoutes, useNotificationTargets } from '../../../api/notifications'
import { useOffHoursTags } from '../../../api/offhours'
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { useToast } from '../../../components/Toast'
import { browserTimezone } from '../offhours/offhours'
import { ScopePicker } from '../notifications/ScopePicker'
import styles from '../notifications/notifications.module.css'
import {
  MAX_REVIEW_ROUTES,
  MAX_REVIEW_SCOPE_ENTRIES,
  REVIEW_SCOPE_GROUPS,
  TYPE_LINES,
  bodyFromDraft,
  draftFromReview,
  isDirty,
  mergeDraft,
  newReviewDraft,
  validateDraft,
  withCadence,
  type ReviewDraft,
} from './reviewDraft'
import { WEEKDAYS, formatInZone, timeZoneOptions } from './schedule'

const PREVIEW_DEBOUNCE_MS = 400
const SPLIT_ADVICE_ABOVE = 150

const messageOf = (error: unknown) => (error instanceof ApiError ? error.message : 'Something went wrong. Try again.')

interface ReviewEditorProps {
  /** The review being edited, or null for a new one. The parent remounts the editor when this changes. */
  review: ReviewDTO | null
  info: AgentReviewsInfo
  /** Called after a new review is saved. */
  onCreated: () => void
}

/** The form for one review, with the preview under it. The draft is kept against the saved review until it is saved. */
export function ReviewEditor({ review, info, onCreated }: ReviewEditorProps) {
  const toast = useToast()
  const list = useAgentReviews()
  const routesQuery = useNotificationRoutes()
  const targets = useNotificationTargets()
  const tags = useOffHoursTags()
  const create = useCreateReview()
  const update = useUpdateReview()

  const otherNames = useMemo(() => (list.data?.items ?? []).filter((other) => other.id !== review?.id).map((other) => other.name), [list.data, review?.id])
  const firstSupported = info.types.find((type) => info.supported_types.includes(type.id)) ?? info.types[0]
  const [saved, setSaved] = useState<ReviewDTO | null>(review)
  const [baseline, setBaseline] = useState<ReviewDraft>(() => (review ? draftFromReview(review) : newReviewDraft(otherNames, browserTimezone(), firstSupported?.id ?? '')))
  const [draft, setDraft] = useState<ReviewDraft>(baseline)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [conflict, setConflict] = useState<string | null>(null)

  const patch = (change: Partial<ReviewDraft>) => {
    setSaveError(null)
    setDraft((current) => ({ ...current, ...change }))
  }

  const problem = validateDraft(draft, info, otherNames)
  const dirty = isDirty(draft, saved ?? undefined)
  const saving = create.isPending || update.isPending

  // Non-PagerDuty routes only: a review sends one message, and PagerDuty has no place for it. A route the review
  // already holds stays listed even if the list no longer carries it.
  const routeChoices = useMemo(() => {
    const listed = (routesQuery.data?.items ?? []).filter((route) => route.destination !== 'pagerduty').map((route) => ({ id: route.id, name: route.name, enabled: route.enabled }))
    const extra = (saved?.routes ?? []).filter((route) => route.destination !== 'pagerduty' && !listed.some((entry) => entry.id === route.id)).map(({ id, name, enabled }) => ({ id, name, enabled }))
    return [...listed, ...extra]
  }, [routesQuery.data, saved])

  // The preview never carries a version; it is asked for after a pause in typing, and only for a draft that could be saved.
  const previewBody = useMemo(() => (problem === null ? bodyFromDraft(draft) : null), [draft, problem])
  const previewSignature = JSON.stringify(previewBody)
  const preview = useReviewPreview()
  const { mutate: requestPreview, reset: resetPreview } = preview
  useEffect(() => {
    if (!previewBody) {
      resetPreview()
      return
    }
    const timer = setTimeout(() => requestPreview(previewBody), PREVIEW_DEBOUNCE_MS)
    return () => clearTimeout(timer)
    // The signature stands for the body: it changes exactly when the request would.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [previewSignature, requestPreview, resetPreview])
  const previewFresh = preview.variables !== undefined && JSON.stringify(preview.variables) === previewSignature
  const previewData = previewFresh ? (preview.data ?? null) : null
  const previewError = previewFresh && preview.error ? messageOf(preview.error) : null

  const submit = () => {
    setSaveError(null)
    if (saved) {
      update.mutate({ id: saved.id, body: bodyFromDraft(draft, saved.version) }, {
        onSuccess: (next) => {
          toast.show(`Saved "${next.name}".`, 'ok')
          const stored = draftFromReview(next)
          setSaved(next)
          setBaseline(stored)
          setDraft(stored)
          setConflict(null)
        },
        onError: (error) => {
          if (error instanceof ApiError && error.status === 409) {
            setConflict(error.message)
            // The fields the admin changed stay as typed; the others follow what is stored now.
            const mine = draft
            const before = baseline
            void list.refetch().then((result) => {
              const current = result.data?.items.find((other) => other.id === saved.id)
              if (!current) {
                setSaveError('This review no longer exists.')
                return
              }
              const stored = draftFromReview(current)
              setSaved(current)
              setBaseline(stored)
              setDraft(mergeDraft(before, mine, stored))
            })
          } else {
            setSaveError(messageOf(error))
          }
        },
      })
    } else {
      create.mutate(bodyFromDraft(draft), {
        onSuccess: (next) => {
          toast.show(`Saved "${next.name}".`, 'ok')
          onCreated()
        },
        onError: (error) => setSaveError(messageOf(error)),
      })
    }
  }

  const supported = new Set(info.supported_types)
  const weekdayNeeded = draft.cadence === 'weekly'
  const dayNeeded = draft.cadence === 'monthly'
  const zones = timeZoneOptions(draft.timezone)
  const toggleRoute = (id: number, on: boolean) => patch({ routeIds: on ? [...draft.routeIds, id] : draft.routeIds.filter((other) => other !== id) })

  const services = previewData?.services_total ?? 0
  const unseen = previewData?.services_unseen ?? 0

  return (
    <Panel title={saved ? 'Edit review' : 'Add review'}>
      <div className={styles.editor}>
        {conflict && (
          <Banner kind="warn">
            <span>{conflict}</span> The stored review is loaded below; your changes are still in the form. Check them, then save again.
          </Banner>
        )}
        <div className={styles.fields}>
          <label className={`${styles.field} ${styles.wide}`}>
            <span>Review name</span>
            <input className={styles.ctl} value={draft.name} maxLength={80} onChange={(event) => patch({ name: event.target.value })} />
          </label>

          <fieldset className={`${styles.field} ${styles.wide}`} style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className={styles.fieldLabel}>Type</legend>
            {info.types.map((type) => {
              const offered = supported.has(type.id)
              const line = TYPE_LINES[type.id]
              return (
                <div key={type.id} className={styles.field}>
                  <label className={styles.check}>
                    <input
                      type="radio"
                      name="review-type"
                      value={type.id}
                      checked={draft.type === type.id}
                      disabled={!offered}
                      aria-describedby={[line ? `review-type-${type.id}` : '', offered ? '' : `review-type-${type.id}-unavailable`].filter(Boolean).join(' ') || undefined}
                      onChange={() => patch({ type: type.id })}
                    />
                    {type.label}
                  </label>
                  {line && (
                    <span id={`review-type-${type.id}`} className={styles.help}>
                      {line}
                    </span>
                  )}
                  {!offered && (
                    <span id={`review-type-${type.id}-unavailable`} className={styles.help}>
                      The agent service does not offer this type yet.
                    </span>
                  )}
                </div>
              )
            })}
            {!info.types.some((type) => type.id === draft.type) && <p className={styles.help}>{`This review has a type this version does not know: ${draft.type}.`}</p>}
          </fieldset>

          <div className={`${styles.field} ${styles.wide}`}>
            <span className={styles.fieldLabel}>Scope</span>
            <ScopePicker
              review
              groups={REVIEW_SCOPE_GROUPS}
              maxEntries={MAX_REVIEW_SCOPE_ENTRIES}
              entries={draft.scope.map((key) => ({ key, threshold: 0 }))}
              saved={saved?.scope ?? []}
              targets={targets.data}
              tags={tags.data}
              onAdd={(key) => patch({ scope: draft.scope.includes(key) ? draft.scope : [...draft.scope, key] })}
              onRemove={(key) => patch({ scope: draft.scope.filter((other) => other !== key) })}
              onThreshold={() => {}}
            />
          </div>

          <label className={styles.field}>
            <span>Cadence</span>
            <select className={styles.ctl} value={draft.cadence} onChange={(event) => setDraft(withCadence(draft, event.target.value as ReviewCadence))}>
              <option value="daily">Daily</option>
              <option value="weekly">Weekly</option>
              <option value="monthly">Monthly</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>Time</span>
            <input className={styles.ctl} type="time" value={draft.time} onChange={(event) => patch({ time: event.target.value })} />
          </label>
          {weekdayNeeded && (
            <label className={styles.field}>
              <span>Weekday</span>
              <select className={styles.ctl} value={String(draft.weekday ?? 1)} onChange={(event) => patch({ weekday: Number(event.target.value) })}>
                {WEEKDAYS.map((day, index) => (
                  <option key={day} value={String(index + 1)}>
                    {day}
                  </option>
                ))}
              </select>
            </label>
          )}
          {dayNeeded && (
            <label className={styles.field}>
              <span>Day of month</span>
              <input className={styles.ctl} type="number" min={1} max={28} step={1} value={draft.monthDay} onChange={(event) => patch({ monthDay: event.target.value })} />
            </label>
          )}
          <label className={styles.field}>
            <span>Time zone</span>
            <select className={styles.ctl} value={draft.timezone} onChange={(event) => patch({ timezone: event.target.value })}>
              {zones.map((zone) => (
                <option key={zone} value={zone}>
                  {zone}
                </option>
              ))}
            </select>
          </label>

          <fieldset className={`${styles.field} ${styles.wide}`} style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className={styles.fieldLabel}>Routes</legend>
            <div className={styles.kinds}>
              {routeChoices.map((route) => (
                <label key={route.id} className={styles.check}>
                  <input type="checkbox" checked={draft.routeIds.includes(route.id)} onChange={(event) => toggleRoute(route.id, event.target.checked)} />
                  {route.name}
                </label>
              ))}
            </div>
            {routesQuery.isPending && <p className={styles.help}>Loading routes.</p>}
            {!routesQuery.isPending && routeChoices.length === 0 && (
              <p className={styles.help}>
                No route can take a review yet. Add a Slack, Telegram, email or webhook route under <Link to="/settings/notifications">Notifications</Link>.
              </p>
            )}
            <p className={styles.help}>{`The result goes to every route you choose, up to ${MAX_REVIEW_ROUTES}. PagerDuty routes are not offered.`}</p>
          </fieldset>

          <label className={`${styles.check} ${styles.wide}`}>
            <input type="checkbox" checked={draft.enabled} onChange={(event) => patch({ enabled: event.target.checked })} />
            Review is on
          </label>
        </div>

        <div className={styles.preview} aria-live="polite">
          {previewError && <p className={styles.error}>{previewError}</p>}
          {previewData && (
            <ul className={styles.lines}>
              <li>{`${services} ${services === 1 ? 'service' : 'services'} in scope.`}</li>
              {unseen > 0 && <li>{`The agent cannot see ${unseen} of them. Change its access under Agent identity.`}</li>}
              {services > SPLIT_ADVICE_ABOVE && <li>One run reads the largest problems first. Split this review by account or tag for more depth.</li>}
              {previewData.next_run_at && <li>{`Next run: ${formatInZone(previewData.next_run_at, draft.timezone)}.`}</li>}
              {previewData.worst_case_month_usd !== null && <li>{`Worst case: $${previewData.worst_case_month_usd} a month at the current model.`}</li>}
            </ul>
          )}
        </div>

        {saveError && (
          <p role="alert" className={styles.error}>
            {saveError}
          </p>
        )}
        <div className={styles.actionsRow}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!dirty || problem !== null || saving} onClick={submit}>
            Save review
          </button>
          <button
            type="button"
            className={styles.btn}
            disabled={!dirty || saving}
            onClick={() => {
              setSaveError(null)
              setConflict(null)
              setDraft(baseline)
            }}
          >
            Discard changes
          </button>
          {dirty && <span className={styles.hint}>Unsaved changes</span>}
        </div>
        {dirty && problem && <p className={styles.error}>{problem}</p>}
      </div>
    </Panel>
  )
}
