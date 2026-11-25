import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ApiError } from '../../../api/client'
import {
  useCreateNotificationRoute,
  useDeleteNotificationRoute,
  useNotificationDeliveries,
  useNotificationPreview,
  useNotificationRoutes,
  useNotificationSummary,
  useNotificationTargets,
  useRetryNotificationDelivery,
  useSendNotificationTest,
  useUpdateNotificationRoute,
  type NotificationPreviewRequest,
  type NotificationRoute,
} from '../../../api/notifications'
import { useOffHoursTags } from '../../../api/offhours'
import { Banner } from '../../../components/Banner'
import { BottomSheet } from '../../../components/BottomSheet'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Panel } from '../../../components/Panel'
import { Seg } from '../../../components/Seg'
import { useToast } from '../../../components/Toast'
import { usePhone } from '../../../components/usePhone'
import { HeaderAction } from '../HeaderAction'
import { browserTimezone } from '../offhours/offhours'
import { DeliveryLog } from './DeliveryLog'
import { RouteEditor } from './RouteEditor'
import { RouteList } from './RouteList'
import { bodyFromDraft, draftFromRoute, failedBanner, isDirty, newRouteDraft, validateDraft, type RouteDraft } from './notifications'
import styles from './notifications.module.css'

const PREVIEW_DEBOUNCE_MS = 400

type Selection = number | 'new'
type View = 'routes' | 'log'

interface DraftState {
  key: string
  baseline: RouteDraft
  draft: RouteDraft
}

function messageOf(error: unknown): string {
  return error instanceof ApiError ? error.message : 'Something went wrong. Try again.'
}

export function NotificationsPage() {
  const toast = useToast()
  const phone = usePhone()
  const [params, setParams] = useSearchParams()
  const view: View = params.get('view') === 'log' ? 'log' : 'routes'
  const logStatus: 'all' | 'failed' = params.get('status') === 'failed' ? 'failed' : 'all'
  const showView = (next: View, status: 'all' | 'failed' = 'all') => {
    const query = new URLSearchParams()
    if (next === 'log') {
      query.set('view', 'log')
      if (status === 'failed') query.set('status', 'failed')
    }
    setParams(query, { replace: true })
  }

  const routesQuery = useNotificationRoutes()
  const targets = useNotificationTargets()
  const tags = useOffHoursTags()
  const summary = useNotificationSummary(true)
  const [removedIds, setRemovedIds] = useState<number[]>([])
  const routes = useMemo(() => (routesQuery.data?.items ?? []).filter((route) => !removedIds.includes(route.id)), [routesQuery.data, removedIds])

  const [picked, setPicked] = useState<Selection | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const selected: Selection | null = picked ?? routes[0]?.id ?? null
  const saved: NotificationRoute | undefined = typeof selected === 'number' ? routes.find((route) => route.id === selected) : undefined

  const [state, setState] = useState<DraftState | null>(null)
  const key = String(selected)
  if (!routesQuery.isPending && selected !== null && state?.key !== key && (selected === 'new' || saved)) {
    const baseline = saved ? draftFromRoute(saved) : newRouteDraft(routes.map((route) => route.name), browserTimezone())
    setState({ key, baseline, draft: baseline })
  }
  const draft = selected !== null && state?.key === key ? state.draft : null
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  const create = useCreateNotificationRoute()
  const update = useUpdateNotificationRoute(typeof selected === 'number' ? selected : 0)
  const remove = useDeleteNotificationRoute()
  const test = useSendNotificationTest(typeof selected === 'number' ? selected : 0)

  // The preview never carries a secret; it is asked for after a pause in typing.
  const previewBody = useMemo<NotificationPreviewRequest | null>(() => {
    if (!draft) return null
    const body: NotificationPreviewRequest = { ...bodyFromDraft(draft, saved) }
    delete body.url
    delete body.routing_key
    delete body.signing_secret
    delete body.bot_token
    delete body.version
    // The preview needs the switch as drafted, changed or not.
    body.investigate = draft.investigate
    if (saved) body.route_id = saved.id
    return body
  }, [draft, saved])
  const preview = useNotificationPreview()
  const { mutate: requestPreview, reset: resetPreview } = preview
  useEffect(() => resetPreview(), [key, resetPreview])
  const previewSignature = JSON.stringify(previewBody)
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
  const previewFresh = (preview.variables?.route_id ?? 'new') === (saved?.id ?? 'new')
  const previewData = previewFresh ? (preview.data ?? null) : null
  const previewError = previewFresh && preview.error ? messageOf(preview.error) : null

  const caps = { emailAvailable: routesQuery.data?.email_available ?? false, secretsAvailable: routesQuery.data?.secrets_available ?? true }
  const problem = draft ? validateDraft(draft, saved, caps) : null
  const dirty = draft ? isDirty(draft, saved) : false

  const choose = (next: Selection) => {
    setSaveError(null)
    setPicked(next)
    setSheetOpen(true)
  }

  const addRoute = () => {
    setSaveError(null)
    setState(null)
    setPicked('new')
    setSheetOpen(true)
    toast.show('Route added. It stays off until you enable it.', 'ok')
  }

  const save = () => {
    if (!state || !draft) return
    setSaveError(null)
    const body = bodyFromDraft(draft, saved)
    const mutation = typeof selected === 'number' ? update : create
    mutation.mutate(body, {
      onSuccess: (route) => {
        toast.show(`Saved "${route.name}".`, 'ok')
        const next = draftFromRoute(route)
        setPicked(route.id)
        setState({ key: String(route.id), baseline: next, draft: next })
      },
      onError: (error) => setSaveError(messageOf(error)),
    })
  }

  const sendTest = () => {
    if (!saved) return
    test.mutate(undefined, {
      onSuccess: (row) => {
        if (row.status === 'delivered') toast.show(`Test sent to ${saved.target}. See the delivery log.`, 'ok')
        else toast.show(row.note || 'The test message failed.', 'bad')
      },
      onError: (error) => toast.show(messageOf(error), 'bad'),
    })
  }

  const removeRoute = () => {
    if (typeof selected !== 'number') return
    const id = selected
    setRemovedIds((ids) => [...ids, id])
    setPicked(null)
    setState(null)
    setSheetOpen(false)
    setConfirmingDelete(false)
    remove.mutate(id, {
      onSuccess: () => toast.show('Route deleted.', 'ok'),
      onError: (error) => {
        setRemovedIds((ids) => ids.filter((other) => other !== id))
        setPicked(id)
        toast.show(messageOf(error), 'bad')
      },
      onSettled: () => setRemovedIds((ids) => ids.filter((other) => other !== id)),
    })
  }

  // The delivery log polls only while it is on screen.
  const deliveries = useNotificationDeliveries(logStatus, view === 'log')
  const rows = useMemo(() => deliveries.data?.pages.flatMap((page) => page.items) ?? [], [deliveries.data])
  const retry = useRetryNotificationDelivery()
  const retried = useRef(new Set<number>())
  useEffect(() => {
    for (const row of rows) {
      if (!retried.current.has(row.id) || row.status === 'pending') continue
      retried.current.delete(row.id)
      if (row.status === 'delivered') toast.show('Delivered on retry.', 'ok')
      else toast.show(row.note || 'The retry did not go through.', 'bad')
    }
  }, [rows, toast])
  const retryRow = (id: number) => {
    retry.mutate(id, {
      onSuccess: () => retried.current.add(id),
      onError: (error) => toast.show(messageOf(error), 'bad'),
    })
  }

  const failed = summary.data?.failed_24h ?? 0
  const editor = draft && selected !== null ? (
    <RouteEditor
      draft={draft}
      onChange={(next) => {
        setSaveError(null)
        setState((current) => (current ? { ...current, draft: next } : current))
      }}
      saved={saved}
      isNew={selected === 'new'}
      kinds={routesQuery.data?.kinds ?? []}
      emailAvailable={caps.emailAvailable}
      secretsAvailable={caps.secretsAvailable}
      settleSeconds={routesQuery.data?.settle_seconds ?? 0}
      targets={targets.data}
      tags={tags.data}
      preview={previewData}
      previewError={previewError}
      problem={problem}
      dirty={dirty}
      saving={create.isPending || update.isPending}
      testing={test.isPending}
      saveError={saveError}
      onSave={save}
      onDiscard={() => {
        setSaveError(null)
        setState((current) => (current ? { ...current, draft: current.baseline } : current))
      }}
      onTest={sendTest}
      onDelete={selected === 'new' ? undefined : () => setConfirmingDelete(true)}
    />
  ) : null

  return (
    <div className={styles.page}>
      <HeaderAction>
        <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={addRoute}>
          Add route
        </button>
      </HeaderAction>
      <div className={styles.head}>
        <h2>Alert notifications</h2>
        <Seg
          label="Notifications view"
          value={view}
          onChange={(next) => showView(next)}
          options={[
            { value: 'routes', label: 'Routes' },
            { value: 'log', label: 'Delivery log' },
          ]}
        />
      </div>
      {failed > 0 && (
        <Banner
          kind="warn"
          action={
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => showView('log', 'failed')}>
              Open delivery log
            </button>
          }
        >
          {`${failedBanner(failed)} A route that keeps failing shows a warning on Settings.`}
        </Banner>
      )}
      {view === 'log' ? (
        <DeliveryLog
          status={logStatus}
          onStatus={(next) => showView('log', next)}
          failedCount={failed}
          rows={rows}
          loading={deliveries.isPending}
          error={deliveries.isError ? messageOf(deliveries.error) : null}
          hasMore={deliveries.hasNextPage}
          loadingMore={deliveries.isFetchingNextPage}
          onLoadMore={() => void deliveries.fetchNextPage()}
          onRetry={retryRow}
          retrying={retry.isPending}
          phone={phone}
        />
      ) : (
        <div className={styles.split}>
          <Panel
            title="Routes"
            flush
            loading={routesQuery.isPending}
            error={routesQuery.isError ? { message: 'Could not load the notification routes.', detail: messageOf(routesQuery.error), onRetry: () => void routesQuery.refetch() } : undefined}
          >
            <RouteList routes={routes} selected={selected ?? -1} newName={selected === 'new' && draft ? draft.name : null} onSelect={choose} />
          </Panel>
          {phone ? (
            <BottomSheet open={sheetOpen && editor !== null} onClose={() => setSheetOpen(false)} label="Route editor" title={selected === 'new' ? 'Add route' : 'Edit route'} tall>
              {editor}
            </BottomSheet>
          ) : (
            (editor || (
              <Panel title="Edit route">
                <p className={styles.listEmpty}>Add a route to start sending alerts.</p>
              </Panel>
            ))
          )}
        </div>
      )}
      {confirmingDelete && (
        <ConfirmDialog
          title={`Delete "${draft?.name ?? ''}"?`}
          confirmLabel="Delete route"
          danger
          confirmDisabled={remove.isPending}
          onConfirm={removeRoute}
          onCancel={() => setConfirmingDelete(false)}
        >
          Its delivery log entries stay.
        </ConfirmDialog>
      )}
    </div>
  )
}
