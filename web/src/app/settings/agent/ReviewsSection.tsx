import { useState } from 'react'
import { Link } from 'react-router'
import { useAgentReviews, useDeleteReview, useRunReviewNow, useUpdateReview, type AgentSettingsDTO, type ReviewDTO } from '../../../api/agent'
import { ApiError } from '../../../api/client'
import { Banner } from '../../../components/Banner'
import { BottomSheet } from '../../../components/BottomSheet'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { Table } from '../../../components/Table'
import { useToast } from '../../../components/Toast'
import { usePhone } from '../../../components/usePhone'
import styles from '../settings.module.css'
import { ReviewEditor } from './ReviewEditor'
import { ReviewRunsTable } from './ReviewRunsTable'
import { inputFromReview } from './reviewDraft'
import { runState } from './runStates'
import { describeSchedule, formatInZone } from './schedule'

type Editing = { kind: 'review'; id: number } | { kind: 'new'; token: number }
type Confirming = { action: 'run' | 'test' | 'delete'; review: ReviewDTO }

const BUSY_TEXT = 'This review is already waiting or running.'
const CUSTOM_OFF_TEXT = 'Custom reviews are turned off by configuration.'

const messageOf = (error: unknown) => (error instanceof ApiError ? error.message : 'Something went wrong. Try again.')

/** The scheduled AI reviews: the list, the editor and the past results of the one being edited. */
export function ReviewsSection({ settings }: { settings: AgentSettingsDTO }) {
  const info = settings.reviews
  const toast = useToast()
  const phone = usePhone()
  const query = useAgentReviews()
  const reviews = query.data?.items ?? []
  const toggle = useUpdateReview()
  const remove = useDeleteReview()
  const runNow = useRunReviewNow()
  const [editing, setEditing] = useState<Editing | null>(null)
  const [confirming, setConfirming] = useState<Confirming | null>(null)
  const [switching, setSwitching] = useState<number | null>(null)

  const typeLabel = (id: string) => info.types.find((type) => type.id === id)?.label ?? id
  const atLimit = reviews.length >= info.max_reviews
  const editedReview = editing?.kind === 'review' ? (reviews.find((review) => review.id === editing.id) ?? null) : null
  const noModel = settings.role_models.reviewer === undefined

  const switchReview = (review: ReviewDTO, enabled: boolean) => {
    setSwitching(review.id)
    toggle.mutate(
      { id: review.id, body: inputFromReview(review, enabled) },
      {
        onError: (error) => {
          toast.show(messageOf(error), 'bad')
          void query.refetch()
        },
        onSettled: () => setSwitching(null),
      },
    )
  }

  const confirmRun = (review: ReviewDTO, test: boolean) => {
    setConfirming(null)
    runNow.mutate({ id: review.id, test }, {
      onSuccess: () => toast.show(test ? 'The test run has started. Its result appears under Past results.' : 'The review has started. Its result goes to its routes.', 'ok'),
      onError: (error) => toast.show(error instanceof ApiError && error.status === 409 ? BUSY_TEXT : messageOf(error), 'bad'),
    })
  }

  const confirmDelete = (review: ReviewDTO) => {
    setConfirming(null)
    remove.mutate(review.id, {
      onSuccess: () => {
        toast.show('Review deleted.', 'ok')
        setEditing((current) => (current?.kind === 'review' && current.id === review.id ? null : current))
      },
      onError: (error) => toast.show(messageOf(error), 'bad'),
    })
  }

  // While the operator's switch is off a custom review can only be deleted.
  const frozen = (review: ReviewDTO) => review.type === 'custom' && !info.custom_enabled
  const rowActions = (review: ReviewDTO) => (
    <>
      {frozen(review) ? (
        <span className={styles.hint}>{CUSTOM_OFF_TEXT}</span>
      ) : (
        <>
          <button type="button" className={styles.btn} aria-label={`Run now ${review.name}`} disabled={runNow.isPending} onClick={() => setConfirming({ action: 'run', review })}>
            Run now
          </button>
          <button type="button" className={styles.btn} aria-label={`Test run ${review.name}`} disabled={runNow.isPending} onClick={() => setConfirming({ action: 'test', review })}>
            Test run
          </button>
      <button type="button" className={styles.btn} aria-label={`Edit ${review.name}`} onClick={() => setEditing({ kind: 'review', id: review.id })}>
            Edit
          </button>
        </>
      )}
      <button type="button" className={styles.btn} aria-label={`Delete ${review.name}`} onClick={() => setConfirming({ action: 'delete', review })}>
        Delete
      </button>
    </>
  )
  const onOff = (review: ReviewDTO) =>
    frozen(review) ? null : (
      <input
        type="checkbox"
        role="switch"
        aria-label={`${review.name} is on`}
        checked={review.enabled}
        disabled={switching === review.id}
        onChange={(event) => switchReview(review, event.target.checked)}
      />
    )
  const nextRun = (review: ReviewDTO) => (review.next_run_at ? formatInZone(review.next_run_at, review.schedule.timezone) : 'Not scheduled')
  const lastResult = (review: ReviewDTO) => {
    const last = review.last_run
    if (!last) return <span className={styles.hint}>No results yet</span>
    const state = runState(last.state)
    const when = last.finished_at ? `, ${new Date(last.finished_at).toLocaleString()}` : ''
    return (
      <Link to={`/reviews/runs/${last.id}`}>
        {state.label}
        {when}
      </Link>
    )
  }

  const editor = editing ? (
    <>
      <ReviewEditor
        key={editing.kind === 'new' ? `new-${editing.token}` : `review-${editing.id}`}
        review={editing.kind === 'review' ? editedReview : null}
        info={info}
        onCreated={() => setEditing(null)}
      />
      {editedReview && <ReviewRunsTable reviewId={editedReview.id} name={editedReview.name} />}
    </>
  ) : null
  // An editor for a review that the list no longer holds (deleted elsewhere) has nothing to edit.
  const showEditor = editor !== null && (editing?.kind === 'new' || editedReview !== null)

  return (
    <>
      <Panel
        title="Reviews"
        loading={query.isPending}
        error={query.isError ? { message: 'Could not load the reviews.', detail: query.error.message, onRetry: () => void query.refetch() } : undefined}
        toolbar={
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={atLimit} onClick={() => setEditing({ kind: 'new', token: Date.now() })}>
            Add review
          </button>
        }
      >
        {!info.enabled && <Banner kind="warn">Reviews are turned off by configuration.</Banner>}
        {noModel && <Banner kind="warn">Set a model for reviews before they can run.</Banner>}
        {info.worst_case_run_usd !== null && <p className={styles.hint}>{`Worst case of one run: $${info.worst_case_run_usd}.`}</p>}
        {atLimit && <p className={styles.hint}>{`You can have up to ${info.max_reviews} reviews. Delete one to add another.`}</p>}
        {reviews.length === 0 ? (
          <p>No reviews yet.</p>
        ) : phone ? (
          <ul aria-label="Reviews" style={{ listStyle: 'none', margin: 0, padding: 0, display: 'grid', gap: 12 }}>
            {reviews.map((review) => (
              <li key={review.id} style={{ display: 'grid', gap: 4 }}>
                <strong>{review.name}</strong>
                <span>{typeLabel(review.type)}</span>
                <span>{describeSchedule(review.schedule)}</span>
                <span className={styles.hint}>{`Next run: ${nextRun(review)}`}</span>
                <span>{lastResult(review)}</span>
                <span className={styles.windowRow}>{onOff(review)}</span>
                <span className={styles.windowRow}>{rowActions(review)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <Table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Type</th>
                <th>Schedule</th>
                <th>Next run</th>
                <th>Last result</th>
                <th>On</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {reviews.map((review) => (
                <tr key={review.id}>
                  <td>{review.name}</td>
                  <td>{typeLabel(review.type)}</td>
                  <td>{describeSchedule(review.schedule)}</td>
                  <td>{nextRun(review)}</td>
                  <td>{lastResult(review)}</td>
                  <td>
                    {onOff(review)} {review.enabled ? <Pill kind="ok">On</Pill> : <Pill kind="mute">Off</Pill>}
                  </td>
                  <td>
                    <span className={styles.windowRow}>{rowActions(review)}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Panel>

      {phone ? (
        <BottomSheet open={showEditor} onClose={() => setEditing(null)} label="Review editor" title={editing?.kind === 'new' ? 'Add review' : 'Edit review'} tall>
          {editor}
        </BottomSheet>
      ) : (
        showEditor && editor
      )}

      {confirming?.action === 'run' && (
        <ConfirmDialog title="Run this review now?" confirmLabel="Run review" onConfirm={() => confirmRun(confirming.review, false)} onCancel={() => setConfirming(null)}>
          It uses the AI allowance and sends the result to its routes.
        </ConfirmDialog>
      )}
      {confirming?.action === 'test' && (
        <ConfirmDialog title="Test run this review?" confirmLabel="Start test run" onConfirm={() => confirmRun(confirming.review, true)} onCancel={() => setConfirming(null)}>
          Run this review once without sending it? It uses the AI allowance. The result appears under Past results.
        </ConfirmDialog>
      )}
      {confirming?.action === 'delete' && (
        <ConfirmDialog
          title="Delete this review?"
          confirmLabel="Delete review"
          danger
          confirmDisabled={remove.isPending}
          onConfirm={() => confirmDelete(confirming.review)}
          onCancel={() => setConfirming(null)}
        >
          Past results stay until they expire.
        </ConfirmDialog>
      )}
    </>
  )
}
