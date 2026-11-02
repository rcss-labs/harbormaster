import type { ReactNode } from 'react'
import type { StaleInfo } from '../../api/types'
import { Panel } from '../../components/Panel'
import { EmptyState } from '../../components/EmptyState'
import { StaleNote } from '../../components/StaleNote'

interface MetricPanelProps {
  title: ReactNode
  right?: ReactNode
  stale?: StaleInfo | null
  /** Shown under the title, like the stale note, e.g. for a partial failure. */
  notice?: ReactNode
  /** Shows `emptyText` instead of the children. */
  empty?: boolean
  emptyText?: string
  children: ReactNode
}

/** A Panel for one metric: stale-data note under the title, and an empty state in place of the chart. */
export function MetricPanel({ title, right, stale, notice, empty = false, emptyText = 'No data yet', children }: MetricPanelProps) {
  return (
    <Panel title={title} actions={right}>
      {stale && <StaleNote stale={stale} />}
      {notice}
      {empty ? <EmptyState>{emptyText}</EmptyState> : children}
    </Panel>
  )
}
