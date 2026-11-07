import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useClusters } from '../../api/hooks'
import type { ClusterCardDTO, ServiceRef, Status, TagDTO } from '../../api/types'
import { Icon } from '../../components/Icon'
import { usePhone } from '../../components/usePhone'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { StatusDot } from '../../components/StatusDot'
import { TagChip } from '../../components/TagChip'
import { Toggle } from '../../components/Toggle'
import { useFilters } from '../shell/useFilters'
import { ActionsSheet, type ActionsTarget } from './ActionsSheet'
import { CLUSTER_CELL_COLOR } from './EstateMap'
import { useEstateState, type EstateGrouping } from './useOverviewState'
import styles from './overview.module.css'

/** Rows a group shows before "Show more". */
const GROUP_ROW_CAP = 12
/** Rows a group shows at most, however often "Show more" is pressed. */
const GROUP_ROW_MAX = 60
/** Groups drawn at once. */
const GROUP_PAGE = 25

/** 0 steady, 1 deploying, 2 degraded or unhealthy, 3 failing. */
const SEVERITY_RANK: Record<Status, number> = {
  steady: 0,
  scaled_to_zero: 0,
  deploying: 1,
  degraded: 2,
  unhealthy_task: 2,
  failing: 3,
}
const GROUP_DOT = ['var(--healthy-cell)', 'var(--info)', 'var(--warn)', 'var(--bad)']

interface EstateService extends ServiceRef {
  status: Status
  rank: number
  clusterName: string
}

interface EstateGroup {
  key: string
  name: string
  where: string
  tag?: TagDTO
  services: EstateService[]
}

function toServices(cluster: ClusterCardDTO): EstateService[] {
  return cluster.services
    .filter((service): service is ServiceRef & { status: Status } => service.status !== '')
    .map((service) => ({
      ...service,
      rank: SEVERITY_RANK[service.status],
      clusterName: cluster.name,
    }))
}

function buildGroups(clusters: ClusterCardDTO[], grouping: EstateGrouping): EstateGroup[] {
  if (grouping === 'cluster') {
    return clusters.map((cluster) => ({
      key: cluster.id,
      name: cluster.name,
      where: `${cluster.account_name} · ${cluster.region}`,
      tag: cluster.tag,
      services: toServices(cluster),
    }))
  }
  const accounts = new Map<string, EstateGroup & { regions: string[] }>()
  for (const cluster of clusters) {
    const group = accounts.get(cluster.account_id) ?? {
      key: cluster.account_id,
      name: cluster.account_name,
      where: '',
      services: [],
      regions: [],
    }
    if (!group.regions.includes(cluster.region)) group.regions.push(cluster.region)
    group.services.push(...toServices(cluster))
    accounts.set(cluster.account_id, group)
  }
  return [...accounts.values()].map((group) => ({
    ...group,
    where: group.regions.join(' · '),
  }))
}

const worstFirst = (a: EstateService, b: EstateService) => b.rank - a.rank || a.name.localeCompare(b.name)

const plural = (count: number, singular: string, pluralWord = `${singular}s`) => `${count} ${count === 1 ? singular : pluralWord}`

/** State that resets when the controls that define it change. */
function useScoped<T>(scope: string, initial: T): [T, (update: (current: T) => T) => void] {
  const [state, setState] = useState<{ scope: string; value: T }>({
    scope,
    value: initial,
  })
  const value = state.scope === scope ? state.value : initial
  return [value, (update) => setState({ scope, value: update(value) })]
}

export function EstateView() {
  const { filters } = useFilters()
  const phone = usePhone()
  const [sheet, setSheet] = useState<ActionsTarget | null>(null)
  const clusters = useClusters(filters).data?.items
  const { grouping, problemsOnly, query, setGrouping, setProblemsOnly, setQuery } = useEstateState()
  const [openByKey, updateOpen] = useScoped<Record<string, boolean>>(grouping, {})
  const [showMore, updateShowMore] = useScoped<Record<string, boolean>>(`${grouping}|${problemsOnly}|${query}`, {})
  const [groupLimit, updateGroupLimit] = useScoped<number>(`${grouping}|${problemsOnly}|${query}`, GROUP_PAGE)

  const needle = query.trim().toLowerCase()
  const view = useMemo(() => {
    const groups = buildGroups(clusters ?? [], grouping)
    const visible: {
      group: EstateGroup
      matching: EstateService[]
      problems: EstateService[]
      severity: number
    }[] = []
    let hiddenGroups = 0
    let totalServices = 0
    for (const group of groups) {
      const matching = group.services.filter(
        (service) =>
          needle === '' ||
          service.name.toLowerCase().includes(needle) ||
          service.clusterName.toLowerCase().includes(needle) ||
          group.name.toLowerCase().includes(needle),
      )
      if (matching.length === 0) continue
      totalServices += matching.length
      const problems = matching.filter((service) => service.rank > 0).sort(worstFirst)
      if (problemsOnly && problems.length === 0) {
        hiddenGroups++
        continue
      }
      visible.push({
        group,
        matching,
        problems,
        severity: problems[0]?.rank ?? 0,
      })
    }
    visible.sort((a, b) => b.severity - a.severity || a.group.name.localeCompare(b.group.name))
    return { visible, hiddenGroups, totalServices }
  }, [clusters, grouping, needle, problemsOnly])

  const noun = grouping === 'cluster' ? 'cluster' : 'account'
  const drawn = view.visible.slice(0, groupLimit)
  const remainingGroups = view.visible.length - drawn.length

  const describe = ({ group, matching, problems, severity }: (typeof drawn)[number]) => {
    const isOpen = openByKey[group.key] ?? (problemsOnly || severity > 0 || needle !== '')
    const ordered = problemsOnly ? problems : [...problems, ...matching.filter((service) => service.rank === 0).sort(worstFirst)]
    const rows = ordered.slice(0, showMore[group.key] ? GROUP_ROW_MAX : GROUP_ROW_CAP)
    return { isOpen, ordered, rows }
  }
  const shownServices = drawn.reduce((total, entry) => {
    const { isOpen, rows } = describe(entry)
    return isOpen ? total + rows.length : total
  }, 0)

  const groupNodes = drawn.map((entry) => {
    const { group, matching, problems, severity } = entry
    const { isOpen, ordered, rows } = describe(entry)
    const moreCount = ordered.length - rows.length
    const steadyHidden = problemsOnly ? matching.length - problems.length : 0

    const failing = matching.filter((service) => service.status === 'failing').length
    const unhealthy = matching.filter((service) => service.rank === 2).length
    const deploying = matching.filter((service) => service.status === 'deploying').length

    return (
      <section key={group.key} className={styles.estateGroup}>
        <button
          type="button"
          className={`${styles.groupHead} ${phone ? styles.groupHeadPhone : ''}`.trim()}
          aria-expanded={isOpen}
          onClick={() => updateOpen((current) => ({ ...current, [group.key]: !isOpen }))}
        >
          <span className={styles.chevron} aria-hidden="true">
            {isOpen ? '▾' : '▸'}
          </span>
          <span className={styles.groupDot} aria-hidden="true" style={{ background: GROUP_DOT[severity] }} />
          <span className={styles.groupName}>{group.name}</span>
          {group.tag && <TagChip tag={group.tag} kind="cluster" />}
          <span className={styles.groupWhere}>{group.where}</span>
          <span className={styles.groupCounts}>
            {failing > 0 && <span style={{ color: 'var(--bad)' }}>{failing} failing</span>}
            {unhealthy > 0 && <span style={{ color: 'var(--warn)' }}>{unhealthy} unhealthy</span>}
            {deploying > 0 && <span style={{ color: 'var(--info)' }}>{deploying} deploying</span>}
            <span>{plural(matching.length, 'svc', 'svc')}</span>
          </span>
        </button>
        {isOpen && (
          <>
            <ul className={styles.estateRows}>
              {rows.map((service) => (
                <li
                  key={service.id}
                  className={`${styles.estateRow} ${phone ? styles.estateRowPhone : ''} ${service.rank > 0 ? styles[`stripe_${service.rank === 3 ? 'bad' : service.rank === 2 ? 'warn' : 'info'}`] : ''}`}
                >
                  <span className={styles.estateService}>
                    <Link to={`/services/${service.id}/overview`} className={styles.whatTitle} data-hm-rl={phone ? '' : undefined}>
                      {service.name}
                    </Link>
                    {grouping === 'account' && <span className={styles.where}>{service.clusterName}</span>}
                  </span>
                  <StatusDot status={service.status} />
                  {phone ? (
                    <button
                      type="button"
                      className={styles.moreBtn}
                      aria-label={`Actions for ${service.name}`}
                      aria-haspopup="dialog"
                      onClick={() =>
                        setSheet({
                          label: service.name,
                          sub: service.clusterName,
                          serviceId: service.id,
                        })
                      }
                    >
                      <Icon name="more" size={18} />
                    </button>
                  ) : (
                    <span className={styles.rowActions}>
                      <Link to={`/services/${service.id}/logs`} className={styles.rowAction}>
                        Logs
                      </Link>
                      <Link to={`/services/${service.id}/tasks`} className={styles.rowAction}>
                        Tasks
                      </Link>
                    </span>
                  )}
                </li>
              ))}
            </ul>
            {(moreCount > 0 || steadyHidden > 0) && (
              <div className={styles.groupNote}>
                {moreCount > 0 && <span>{plural(moreCount, 'more service')} · filter to narrow down</span>}
                {moreCount > 0 && !showMore[group.key] && ordered.length > GROUP_ROW_CAP && (
                  <button
                    type="button"
                    className={styles.linkButton}
                    onClick={() =>
                      updateShowMore((current) => ({
                        ...current,
                        [group.key]: true,
                      }))
                    }
                  >
                    Show more
                  </button>
                )}
                {steadyHidden > 0 && <span>{plural(steadyHidden, 'steady service')} hidden</span>}
              </div>
            )}
          </>
        )}
      </section>
    )
  })

  const toolbar = (
    <>
      <span className={styles.toolbarLabel}>Group by</span>
      <SegmentedFilter
        label="Group by"
        value={grouping}
        onChange={setGrouping}
        options={[
          { value: 'cluster', label: 'Cluster' },
          { value: 'account', label: 'Account' },
        ]}
      />
      <Toggle checked={problemsOnly} onChange={setProblemsOnly}>
        Problems only
      </Toggle>
      <input
        type="text"
        className={styles.estateFilter}
        data-hm-filter
        aria-label="Filter services or clusters"
        placeholder="Filter services or clusters"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
    </>
  )

  return (
    <Panel
      title="Services"
      toolbar={toolbar}
      actions={clusters ? <span className={styles.summary}>{`${shownServices} of ${view.totalServices} services shown`}</span> : undefined}
      flush
    >
      <div className={styles.estateScroll}>
        <div className={styles.estateTable}>
          {!clusters && <EmptyState>Loading services.</EmptyState>}
          {clusters && view.visible.length === 0 && (
            <EmptyState>{view.hiddenGroups > 0 && needle === '' ? 'No services have problems.' : 'No services match.'}</EmptyState>
          )}
          {groupNodes}
          {remainingGroups > 0 && (
            <button type="button" className={styles.showAll} onClick={() => updateGroupLimit((current) => current + GROUP_PAGE)}>
              {`Show ${Math.min(GROUP_PAGE, remainingGroups)} more ${noun}s`}
            </button>
          )}
          {problemsOnly && view.hiddenGroups > 0 && (
            <div className={styles.hiddenNote}>
              <span className={styles.groupDot} aria-hidden="true" style={{ background: CLUSTER_CELL_COLOR.healthy }} />
              <span>{`${plural(view.hiddenGroups, `healthy ${noun}`, `healthy ${noun}s`)} hidden`}</span>
              <button type="button" className={styles.linkButton} onClick={() => setProblemsOnly(false)}>
                Show all
              </button>
            </div>
          )}
        </div>
      </div>
      {sheet && <ActionsSheet target={sheet} isAdmin={false} onClose={() => setSheet(null)} />}
    </Panel>
  )
}
