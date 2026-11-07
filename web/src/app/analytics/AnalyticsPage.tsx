import { useState, type ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router'
import { useAnalytics, type AnalyticsRange, type AnalyticsResponse } from '../../api/analytics'
import { Banner } from '../../components/Banner'
import { Chips } from '../../components/Chips'
import { Disclosure } from '../../components/Disclosure'
import { EmptyState } from '../../components/EmptyState'
import { SegmentedFilter } from '../../components/Seg'
import { Tabs, type TabItem } from '../../components/Tabs'
import { Toggle } from '../../components/Toggle'
import { useToast } from '../../components/Toast'
import { usePhone } from '../../components/usePhone'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import { visibleTagIds } from '../shell/useFilters'
import { CapacityTab } from './CapacityTab'
import { DeliveryTab } from './DeliveryTab'
import { retentionText, windowText } from './format'
import { OperationsTab } from './OperationsTab'
import { ReliabilityTab } from './ReliabilityTab'
import { useAnalyticsPrefs } from './prefs'
import { ANALYTICS_RANGES, ANALYTICS_TABS, useAnalyticsState, type AnalyticsPageTab } from './useAnalyticsState'
import styles from './analytics.module.css'

const TAB_META: Record<AnalyticsPageTab, { label: string; window: string }> = {
  delivery: { label: 'Delivery', window: '90d' },
  reliability: { label: 'Reliability', window: '7d' },
  capacity: { label: 'Capacity', window: 'live + 7d' },
  operations: { label: 'Operations', window: '90d' },
}

const TAB_ITEMS: TabItem[] = ANALYTICS_TABS.map((tab) => ({
  key: tab,
  label: TAB_META[tab].label,
  badge: <span className={styles.window}>{TAB_META[tab].window}</span>,
}))

const CAPACITY_NOTICE =
  "Breakdowns use the latest poll. The 7-day trend is estimated from task-count history and each service's current task definition, so the range filter does not apply."

/** `/analytics` goes to the first tab and keeps the filters in the query string. */
export function AnalyticsRedirect() {
  const { search } = useLocation()
  return <Navigate to={`/analytics/delivery${search}`} replace />
}

export function AnalyticsPage() {
  const { tab } = useAnalyticsState()
  if (tab === null) return <AnalyticsRedirect />
  return <AnalyticsView tab={tab} />
}

function AnalyticsView({ tab }: { tab: AnalyticsPageTab }) {
  useCrumbs([['Analytics', '/analytics/delivery'], [TAB_META[tab].label]])
  const { range, setRange, tag, setTags } = useAnalyticsState()
  const { me } = useAuth()
  const toast = useToast()
  const { search } = useLocation()
  const [prefs, setPrefs] = useAnalyticsPrefs()
  const phone = usePhone()
  const [displayOpen, setDisplayOpen] = useState(false)
  const analytics = useAnalytics(tab, range)
  const data = analytics.data

  const visibleTags = me ? me.tags.filter((t) => visibleTagIds(me).includes(t.id)) : []
  const selectedTag = tag?.length === 1 ? String(tag[0]) : tag?.length ? '' : 'all'
  const rangeFixed = tab === 'capacity'

  const notice = rangeFixed
    ? CAPACITY_NOTICE
    : data?.window.clamped
      ? `Showing the last ${windowText(data.window.used)}. Stopped tasks and task-count history are kept ${retentionText(data.window.retention)}.`
      : null

  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1 className="hm-phone-title-sr">Analytics</h1>
          <div className={styles.meta}>Delivery, reliability, capacity and operations, across your clusters and services.</div>
        </div>
        <div className={styles.filters}>
          {phone ? (
            <Chips
              label="Tag"
              value={selectedTag}
              onChange={(value) => setTags(value === 'all' ? [] : [Number(value)])}
              options={[{ value: 'all', label: 'All' }, ...visibleTags.map((t) => ({ value: String(t.id), label: t.name }))]}
            />
          ) : (
            <SegmentedFilter
              label="Tag"
              value={selectedTag}
              onChange={(value) => setTags(value === 'all' ? [] : [Number(value)])}
              options={[{ value: 'all', label: 'All' }, ...visibleTags.map((t) => ({ value: String(t.id), label: t.name }))]}
            />
          )}
          <div className={rangeFixed ? styles.dimmed : undefined}>
            <SegmentedFilter
              label="Range"
              disabled={rangeFixed}
              value={range}
              onChange={(value: AnalyticsRange) => (rangeFixed ? toast.show('This page does not use the range filter') : setRange(value))}
              options={ANALYTICS_RANGES.map((r) => ({ value: r, label: r }))}
            />
          </div>
        </div>
      </div>
      <div className={styles.tabRow}>
        <div className={styles.tabs}>
          <Tabs items={TAB_ITEMS} active={tab} hrefFor={(key) => `/analytics/${key}${search}`} />
        </div>
        <div className={styles.display}>
          <Disclosure label="Display" expanded={displayOpen} onToggle={() => setDisplayOpen((open) => !open)} />
          {displayOpen && (
            <div className={styles.displayMenu}>
              <Toggle checked={prefs.showSources} onChange={(showSources) => setPrefs({ showSources })}>
                Show data sources
              </Toggle>
            </div>
          )}
        </div>
      </div>
      {notice !== null && <Banner kind="warn">{notice}</Banner>}
      {data ? (
        <TabBody data={data} showSources={prefs.showSources} />
      ) : analytics.isError ? (
        <EmptyState>{analytics.error instanceof Error ? analytics.error.message : 'The analytics could not be loaded.'}</EmptyState>
      ) : null}
    </div>
  )
}

function TabBody({ data, showSources }: { data: AnalyticsResponse; showSources: boolean }): ReactNode {
  const props = { showSources }
  switch (data.tab) {
    case 'delivery':
      return <DeliveryTab data={data} {...props} />
    case 'reliability':
      return <ReliabilityTab data={data} {...props} />
    case 'capacity':
      return <CapacityTab data={data} {...props} />
    case 'operations':
      return <OperationsTab data={data} {...props} />
  }
}
