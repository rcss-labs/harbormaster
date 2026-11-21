import { useState } from 'react'
import type { AnalyticsCost, CostGroup } from '../../api/analytics'
import { Banner } from '../../components/Banner'
import { KpiStrip } from '../../components/KpiStrip'
import { SegmentedFilter } from '../../components/Seg'
import { AnalyticsPanel } from './AnalyticsPanel'
import { rightsizeHref } from '../services/deploy/rightsizeParams'
import { spotHref, spotReason } from '../services/deploy/capacityParams'
import { DraftChangeSetsDialog, draftNote, type DraftRow } from './DraftChangeSetsDialog'
import { RightsizingPanel } from './RightsizingPanel'
import { SpotPanel } from './SpotPanel'
import { costSummary, draftTitle, money, selectedSpot, selectedSuggestions } from './cost'
import { COLOR, sourceFor, whole } from './format'
import { KpiNote, SourcesPanel, type TabProps } from './parts'
import styles from './analytics.module.css'

type Grouping = 'cluster' | 'account' | 'tag'

export function CostTab({ data, showSources }: TabProps<AnalyticsCost>) {
  const { kpis, rightsizing, spot } = data
  const [grouping, setGrouping] = useState<Grouping>('cluster')
  // Kept here, not in the panel, so a refetch does not reset the slider, the selection or the opened marks.
  const [headroomPct, setHeadroomPct] = useState(rightsizing.default_headroom_pct)
  const [excluded, setExcluded] = useState<ReadonlySet<string>>(new Set())
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set())
  const [spotChoices, setSpotChoices] = useState<ReadonlyMap<string, boolean>>(new Map())
  const [spotOpened, setSpotOpened] = useState<ReadonlySet<string>>(new Set())
  const [drafting, setDrafting] = useState<'rightsizing' | 'spot' | null>(null)
  const suggested = selectedSuggestions(rightsizing, headroomPct, excluded)
  const spotSelected = selectedSpot(spot, spotChoices)
  // The dialog follows the live selection; with nothing left to draft it closes.
  if (drafting === 'rightsizing' && suggested.length === 0) setDrafting(null)
  if (drafting === 'spot' && spotSelected.length === 0) setDrafting(null)
  const rightsizingRows: DraftRow[] = suggested.map(({ service, suggestion }) => ({
    serviceId: service.service_id,
    name: service.service,
    now: `${service.cpu_units} / ${service.memory_mib}`,
    proposed: `${suggestion.cpu_units} / ${suggestion.memory_mib}`,
    href: rightsizeHref(service.service_id, { cpuUnits: suggestion.cpu_units, memoryMib: suggestion.memory_mib }, headroomPct),
  }))
  const spotRows: DraftRow[] = spotSelected.map((service) => ({
    serviceId: service.service_id,
    name: service.service,
    now: 'FARGATE',
    proposed: `FARGATE base ${service.on_demand_base}, FARGATE_SPOT weight 1`,
    href: spotHref(service.service_id, service.on_demand_base),
  }))
  const toggleIn = (set: ReadonlySet<string>, id: string) => {
    const next = new Set(set)
    if (!next.delete(id)) next.add(id)
    return next
  }
  const spotSaving = spotSelected.reduce((total, service) => total + (service.saving_monthly ?? 0), 0)
  const spotCandidateCount = spot.services.filter((service) => service.status === 'candidate').length
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
                {!nothingPriced && kpis.spot_monthly > 0 && <KpiNote>{`includes ${money(kpis.spot_monthly)} of Spot tasks at Spot prices`}</KpiNote>}
              </>
            ),
          },
          {
            label: `possible monthly saving at ${headroomPct}% headroom`,
            value: ready ? `${summary.includesSpot ? 'about ' : ''}${money(summary.saving)}` : '—',
          },
          { label: 'of spend is reserved but not needed', value: ready ? `${notNeededPct}%` : '—' },
          { label: summary.services === 1 ? 'service can use a smaller size' : 'services can use a smaller size', value: ready ? whole(summary.services) : '—' },
          // Savings are measured against how services run today, so this one is never added to the rightsizing figure.
          { label: 'possible monthly saving on Fargate Spot', value: spotCandidateCount > 0 ? `about ${money(spotSaving)}` : '—' },
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
            onToggle={(serviceId) => setExcluded((previous) => toggleIn(previous, serviceId))}
            onDraft={() => setDrafting('rightsizing')}
          />
        </div>
        <div className={styles.full}>
          <SpotPanel
            spot={spot}
            pricesAsOf={data.spot_prices_as_of}
            choices={spotChoices}
            onChoose={(serviceId, ticked) => setSpotChoices((previous) => new Map(previous).set(serviceId, ticked))}
            onDraft={() => setDrafting('spot')}
          />
        </div>
        <SourcesPanel sources={data.sources} {...common} />
      </div>
      {drafting === 'rightsizing' && (
        <DraftChangeSetsDialog
          title={draftTitle(rightsizingRows.length)}
          sub="CPU units, memory MiB"
          note={draftNote('the size', `Rightsizing at ${headroomPct}% headroom`)}
          rows={rightsizingRows}
          opened={opened}
          onOpened={(serviceId) => setOpened((previous) => new Set(previous).add(serviceId))}
          onClose={() => setDrafting(null)}
        />
      )}
      {drafting === 'spot' && (
        <DraftChangeSetsDialog
          title={draftTitle(spotRows.length)}
          sub="Capacity providers"
          note={draftNote('the capacity providers', spotReason(spotSelected[0]?.on_demand_base ?? 1))}
          rows={spotRows}
          opened={spotOpened}
          onOpened={(serviceId) => setSpotOpened((previous) => new Set(previous).add(serviceId))}
          onClose={() => setDrafting(null)}
        />
      )}
    </>
  )
}
