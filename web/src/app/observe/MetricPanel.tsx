import type { ReactNode } from 'react'
import type { StaleInfo } from '../../api/types'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { StaleNote } from '../../components/StaleNote'

interface MetricPanelProps {
  title: ReactNode
  right?: ReactNode
  stale?: StaleInfo | null
  /** Shows `emptyText` instead of the children. */
  empty?: boolean
  emptyText?: string
  children: ReactNode
}

/** A Card for one metric: stale-data note under the title, and an empty state in place of the chart. */
export function MetricPanel({ title, right, stale, empty = false, emptyText = 'No data yet', children }: MetricPanelProps) {
  return (
    <Card title={title} right={right}>
      {stale && <StaleNote stale={stale} />}
      {empty ? <EmptyState>{emptyText}</EmptyState> : children}
    </Card>
  )
}
