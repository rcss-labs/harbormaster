import { useEffect, useMemo, useState } from 'react'
import { useService, useServicesList } from '../../../api/hooks'
import type { ServiceListItemDTO } from '../../../api/types'
import { ColumnPicker, useColumnPrefs } from '../../../components/ColumnPicker'
import { DataBanner } from '../../../components/DataBanner'
import { Panel } from '../../../components/Panel'
import { Seg, SegmentedFilter } from '../../../components/Seg'
import { useToast } from '../../../components/Toast'
import { useAuth } from '../../auth/AuthProvider'
import { useCrumbs } from '../../shell/crumbs'
import { usePins } from '../../shell/pins'
import { useFilters, visibleTagIds } from '../../shell/useFilters'
import { RestartDialog, restartBlockedTitle } from '../RestartDialog'
import {
  VIEW_ALL,
  VIEW_PINS,
  VIEW_PROBLEMS,
  applyQuery,
  applyStatus,
  applyView,
  sortServices,
  statusCounts,
  tagView,
  type ServiceSortKey,
  type ServiceStatusFilter,
} from './filterServices'
import { ServiceDrawer } from './ServiceDrawer'
import { COLUMN_KEYS, COLUMN_LABELS, DEFAULT_COLUMNS, ServicesTable } from './ServicesTable'
import styles from './services.module.css'
import { useServicesState } from './useServicesState'

/** The server caps the list at this many services. */
const LIST_CAP = 2000

const COLUMN_CHOICES = COLUMN_KEYS.map((key) => ({ key, label: COLUMN_LABELS[key], locked: key === 'name' }))

/** Loads the full service when a row asks for a restart, then shows the usual confirm dialog. */
function RestartFlow({ service, onClose }: { service: ServiceListItemDTO; onClose: () => void }) {
  const full = useService(service.id)
  const toast = useToast()
  const blocked = full.data ? restartBlockedTitle(full.data) : undefined
  useEffect(() => {
    if (!blocked) return
    toast.show(blocked, 'bad')
    onClose()
    // Show the reason once per opened dialog.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [blocked])
  useEffect(() => {
    if (!full.isError) return
    toast.show('Restart failed.', 'bad')
    onClose()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [full.isError])
  if (!full.data || blocked) return null
  return <RestartDialog serviceId={service.id} service={full.data} onClose={onClose} />
}

export function ServicesPage() {
  useCrumbs([['Services']])
  const { filters: scope } = useFilters()
  const list = useServicesList(scope)
  const { me } = useAuth()
  const { pins, isPinned, toggle } = usePins()
  const state = useServicesState()
  const [columns, setColumns] = useColumnPrefs('harbormaster-columns:services', DEFAULT_COLUMNS)
  const [detailId, setDetailId] = useState<string>()
  const [restarting, setRestarting] = useState<ServiceListItemDTO>()

  const items = useMemo(() => list.data?.items ?? [], [list.data])
  const pinnedIds = useMemo(() => new Set(pins.filter((pin) => pin.kind === 'service').map((pin) => pin.id)), [pins])
  const visibleTags = me ? visibleTagIds(me).flatMap((id) => me.tags.find((tag) => tag.id === id) ?? []) : []

  const views = [
    { value: VIEW_ALL, label: 'All', count: items.length },
    { value: VIEW_PROBLEMS, label: 'Problems', count: applyView(items, VIEW_PROBLEMS, pinnedIds).length },
    { value: VIEW_PINS, label: 'My pins', count: applyView(items, VIEW_PINS, pinnedIds).length },
    ...visibleTags.map((tag) => ({ value: tagView(tag.id), label: tag.name, count: applyView(items, tagView(tag.id), pinnedIds).length })),
  ]
  // A link to a view that no longer exists (a tag the user cannot see) shows everything.
  const view = views.some((v) => v.value === state.view) ? state.view : VIEW_ALL

  const inView = applyQuery(applyView(items, view, pinnedIds), state.q)
  const counts = statusCounts(inView)
  const shown = sortServices(applyStatus(inView, state.status), state.sort.key, state.sort.dir)
  const sortLabel = state.sort.key === 'name' ? 'service' : COLUMN_LABELS[state.sort.key].toLowerCase()
  const detail = items.find((service) => service.id === detailId)

  const statusOptions: { value: ServiceStatusFilter; label: string }[] = [
    { value: 'any', label: 'All' },
    { value: 'bad', label: 'Failing' },
    { value: 'warn', label: 'Warning' },
    { value: 'info', label: 'In progress' },
    { value: 'ok', label: 'Steady' },
    { value: 'mute', label: 'Scaled to 0' },
  ]

  const empty = (
    <span className={styles.empty}>
      No services match these filters.
      <button type="button" className={styles.btn} onClick={state.clearAll}>
        Clear filters
      </button>
    </span>
  )

  const startRestart = (service: ServiceListItemDTO) => {
    setDetailId(undefined)
    setRestarting(service)
  }

  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <h1>Services</h1>
        <p className={styles.sub}>Every ECS service you can see, across accounts and regions. Click a name to open it.</p>
      </div>
      <DataBanner tiers={['Clusters and services']} />
      <Panel
        title="Services"
        label="Services"
        flush
        loading={list.isPending}
        error={
          list.isError
            ? { message: 'The services could not be loaded.', detail: list.error instanceof Error ? list.error.message : undefined, onRetry: () => void list.refetch() }
            : undefined
        }
        footer={
          <div className={styles.foot}>
            <span>
              {shown.length} of {items.length} services
            </span>
            <span>Sorted by {sortLabel}. Dashed tags are inherited from the cluster.</span>
          </div>
        }
      >
        <div className={styles.views}>
          <Seg label="Saved views" options={views} value={view} onChange={state.setView} />
        </div>
        <div className={styles.toolbar}>
          <input
            type="search"
            className={styles.search}
            data-hm-filter
            aria-label="Filter services"
            placeholder="Filter by service, cluster, account, region or image"
            value={state.q}
            onChange={(event) => state.setQ(event.target.value)}
          />
          <SegmentedFilter
            label="Status"
            value={state.status}
            onChange={state.setStatus}
            options={statusOptions.map((option) => ({ ...option, count: counts[option.value] }))}
          />
          <span className={styles.pushRight}>
            <ColumnPicker columns={COLUMN_CHOICES} visible={columns} onChange={setColumns} defaults={DEFAULT_COLUMNS} />
          </span>
        </div>
        <ServicesTable
          services={shown}
          visibleColumns={columns}
          sort={state.sort}
          onSortChange={(next) => state.setSort({ key: next.key as ServiceSortKey, dir: next.dir })}
          page={state.page}
          onPageChange={state.setPage}
          isPinned={(id) => isPinned('service', id)}
          onTogglePin={(service) => toggle({ kind: 'service', id: service.id, name: service.name, cluster: service.cluster.name })}
          onDetails={(service) => setDetailId(service.id)}
          onRestart={startRestart}
          empty={empty}
        />
      </Panel>
      {list.data?.truncated && (
        <p className={styles.note}>Only the first {LIST_CAP} services are listed. Narrow the scope at the top to see the rest.</p>
      )}
      <ServiceDrawer service={detail} onClose={() => setDetailId(undefined)} onRestart={startRestart} />
      {restarting && <RestartFlow service={restarting} onClose={() => setRestarting(undefined)} />}
    </div>
  )
}
