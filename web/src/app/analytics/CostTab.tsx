import { useState } from 'react'
import type { AnalyticsCost, CostGroup } from '../../api/analytics'
import { Banner } from '../../components/Banner'
import { KpiStrip } from '../../components/KpiStrip'
import { SegmentedFilter } from '../../components/Seg'
import { AnalyticsPanel } from './AnalyticsPanel'
import { DraftChangeSetsDialog } from './DraftChangeSetsDialog'
import { RightsizingPanel } from './RightsizingPanel'
import { costSummary, money, type SuggestedService } from './cost'
import { COLOR, sourceFor, whole } from './format'
import { KpiNote, SourcesPanel, type TabProps } from './parts'
import styles from './analytics.module.css'

type Grouping = 'cluster' | 'account' | 'tag'

export function CostTab({ data, showSources }: TabProps<AnalyticsCost>) {
  const { kpis, rightsizing } = data
  const [grouping, setGrouping] = useState<Grouping>('cluster')
  // Kept here, not in the panel, so a refetch does not reset the slider, the selection or the opened marks.
  const [headroomPct, setHeadroomPct] = useState(rightsizing.default_headroom_pct)
  const [excluded, setExcluded] = useState<ReadonlySet<string>>(new Set())
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set())
  const [drafting, setDrafting] = useState<SuggestedService[] | null>(null)
  const common = { window: data.window, showSources }

  const ready = rightsizing.state === 'ok'
  const summary = costSummary(rightsizing.services, headroomPct)
  // Savings use the current definition and spend uses each task's own size, so a saving can exceed spend during an upsizing rollout.
  const notNeededPct = kpis.monthly_total > 0 ? Math.min(100, Math.round((summary.saving / kpis.monthly_total) * 100)) : 0
  const groups: Record<Grouping, CostGroup[]> = { cluster: data.by_cluster, account: data.by_account, tag: data.by_tag }
  const rows = groups[grouping]
  // With every task unpriced, $0 would read as free.
  const nothingPriced = kpis.monthly_total === 0 && kpis.unpriced_tasks > 0
  const largest = Math.max(0, ...rows.map((group) => group.monthly))

  return (
    <>
      <KpiStrip
        label="Cost key figures"
        items={[
          {
            label: 'estimated Fargate spend per month',
            value: (
              <>
                {nothingPriced ? '—' : money(kpis.monthly_total)}
                {!nothingPriced && kpis.spot_monthly > 0 && <KpiNote>{`includes ${money(kpis.spot_monthly)} of Spot tasks at list price`}</KpiNote>}
              </>
            ),
          },
          {
            label: `possible monthly saving at ${headroomPct}% headroom`,
            value: ready ? `${summary.upperBound ? 'up to ' : ''}${money(summary.saving)}` : '—',
          },
          { label: 'of spend is reserved but not needed', value: ready ? `${notNeededPct}%` : '—' },
          { label: summary.services === 1 ? 'service can use a smaller size' : 'services can use a smaller size', value: ready ? whole(summary.services) : '—' },
        ]}
      />
      {kpis.unpriced_tasks > 0 && <Banner kind="warn">{`${kpis.unpriced_tasks === 1 ? '1 task is' : `${kpis.unpriced_tasks} tasks are`} not priced (EC2, other non-Fargate capacity, or a region without a price table).`}</Banner>}
      {kpis.unpriced_regions.length > 0 && <Banner kind="warn">{`No price table for region ${kpis.unpriced_regions.join(', ')}. Those tasks are not priced.`}</Banner>}
      <div className={styles.grid}>
        <div className={styles.full}>
          <AnalyticsPanel title="Monthly spend" source={sourceFor(data.sources, 'tasks')} {...common}>
            <div className={styles.spendTools}>
              <SegmentedFilter
                label="Group by"
                value={grouping}
                onChange={setGrouping}
                options={[
                  { value: 'cluster', label: 'Cluster' },
                  { value: 'account', label: 'Account' },
                  { value: 'tag', label: 'Tag' },
                ]}
              />
            </div>
            {rows.length === 0 ? (
              <p className={styles.kpiNote}>No services to show.</p>
            ) : (
              <ul className={styles.spendList}>
                {rows.map((group) => {
                  // A group of only unpriced tasks has no spend to show, and $0 would read as free.
                  const notPriced = group.monthly === 0 && group.unpriced_tasks > 0
                  return (
                    <li key={group.key} className={styles.spendRow}>
                      <span className={styles.spendLabel}>{group.label}</span>
                      <span className={styles.spendTrack} aria-hidden="true">
                        <i style={{ width: `${largest > 0 ? (group.monthly / largest) * 100 : 0}%`, background: COLOR.accent }} />
                      </span>
                      <span className={styles.spendAmount}>{notPriced ? 'not priced' : money(group.monthly)}</span>
                    </li>
                  )
                })}
              </ul>
            )}
          </AnalyticsPanel>
        </div>
        <div className={styles.full}>
          <RightsizingPanel
            rightsizing={rightsizing}
            headroomPct={headroomPct}
            onHeadroomChange={setHeadroomPct}
            excluded={excluded}
            onToggle={(serviceId) =>
              setExcluded((previous) => {
                const next = new Set(previous)
                if (!next.delete(serviceId)) next.add(serviceId)
                return next
              })
            }
            onDraft={setDrafting}
          />
        </div>
        <SourcesPanel sources={data.sources} {...common} />
      </div>
      {drafting !== null && (
        <DraftChangeSetsDialog
          rows={drafting}
          headroomPct={headroomPct}
          opened={opened}
          onOpened={(serviceId) => setOpened((previous) => new Set(previous).add(serviceId))}
          onClose={() => setDrafting(null)}
        />
      )}
    </>
  )
}
