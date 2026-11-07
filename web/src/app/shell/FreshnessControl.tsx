import { useState } from 'react'
import { Link } from 'react-router'
import type { PollerEntryDTO, PollerTierDTO } from '../../api/types'
import { useDataProblems } from '../../api/observe'
import { usePollerStatus } from '../../api/hooks'
import { Disclosure } from '../../components/Disclosure'
import { Popover } from '../../components/Popover'
import { StatusDot } from '../../components/StatusDot'
import { useAuth } from '../auth/AuthProvider'
import styles from './shell.module.css'

/** One data type of a region after tiers that share a label are merged (worst state wins). */
interface DataType {
  label: string
  core: boolean
  failing: boolean
  throttled: boolean
  /** Oldest successful update among the merged tiers; null when one has never succeeded. */
  lastOkAt: string | null
  lastError: string
}

function mergeTiers(tiers: PollerTierDTO[]): DataType[] {
  const byLabel = new Map<string, DataType>()
  for (const tier of tiers) {
    const existing = byLabel.get(tier.label)
    if (!existing) {
      byLabel.set(tier.label, {
        label: tier.label,
        core: tier.core,
        failing: tier.failing,
        throttled: tier.throttled,
        lastOkAt: tier.last_ok_at,
        lastError: tier.failing ? tier.last_error : '',
      })
      continue
    }
    existing.core ||= tier.core
    existing.failing ||= tier.failing
    existing.throttled ||= tier.throttled
    if (tier.last_ok_at === null || existing.lastOkAt === null) existing.lastOkAt = null
    else if (Date.parse(tier.last_ok_at) < Date.parse(existing.lastOkAt)) existing.lastOkAt = tier.last_ok_at
    if (!existing.lastError && tier.failing) existing.lastError = tier.last_error
  }
  return [...byLabel.values()]
}

const minutesSince = (iso: string) => Math.max(1, Math.floor((Date.now() - Date.parse(iso)) / 60_000))

/** "Auto scaling is 4 min old", "Auto scaling is unavailable" or "Auto scaling updates are slowed by AWS throttling". */
function problemText(type: DataType): string {
  if (type.failing) return type.lastOkAt ? `${type.label} is ${minutesSince(type.lastOkAt)} min old` : `${type.label} is unavailable`
  return `${type.label} updates are slowed by AWS throttling`
}

/** The worst state across regions: a failing core data type is bad, anything else that is not current is a warning. */
function worstKind(items: PollerEntryDTO[]): 'ok' | 'warn' | 'bad' {
  let worst: 'ok' | 'warn' | 'bad' = 'ok'
  for (const item of items) {
    for (const type of mergeTiers(item.tiers)) {
      if (type.failing && type.core) return 'bad'
      if (type.failing || type.throttled) worst = 'warn'
    }
  }
  return worst
}

function RegionRow({ item, canSeeError }: { item: PollerEntryDTO; canSeeError: boolean }) {
  const [errorsShown, setErrorsShown] = useState(false)
  const types = mergeTiers(item.tiers)
  const stale = types.filter((type) => type.failing || type.throttled)
  const current = types.filter((type) => !type.failing && !type.throttled)
  const errors = stale.filter((type) => type.failing && type.lastError)
  return (
    <div className={styles.freshRegion}>
      <StatusDot kind={stale.length === 0 ? 'ok' : stale.some((type) => type.failing && type.core) ? 'bad' : 'warn'}>
        {item.account_name} / {item.region}
      </StatusDot>
      {stale.map((type) => (
        <span key={type.label}>{problemText(type)}</span>
      ))}
      {current.length > 0 && <span className={styles.freshCurrent}>Current: {current.map((type) => type.label).join(', ')}</span>}
      {canSeeError && errors.length > 0 && (
        <>
          <Disclosure label="Show AWS error" expanded={errorsShown} onToggle={() => setErrorsShown((shown) => !shown)} />
          {errorsShown && (
            <pre className={styles.freshError}>
              {errors.map((type) => `${type.label}: ${type.lastError}`).join('\n')}
            </pre>
          )}
        </>
      )}
    </div>
  )
}

/** "auto scaling stale in 3 regions": non-core data types that fail or are throttled, counted by regions. */
function nonCoreNote(items: PollerEntryDTO[]): string {
  const regionsByLabel = new Map<string, number>()
  for (const item of items) {
    for (const type of mergeTiers(item.tiers)) {
      if (!type.core && (type.failing || type.throttled)) regionsByLabel.set(type.label, (regionsByLabel.get(type.label) ?? 0) + 1)
    }
  }
  return [...regionsByLabel].map(([label, regions]) => `${label.toLowerCase()} stale in ${regions} ${regions === 1 ? 'region' : 'regions'}`).join('; ')
}

/** "N of M regions current": a region is current when none of its core data types fails or is throttled. */
export function FreshnessControl() {
  const { me } = useAuth()
  const poller = usePollerStatus()
  const { current, total } = useDataProblems()
  const [open, setOpen] = useState(false)
  const items = poller.data?.items ?? []
  if (!poller.data) return null
  const admin = me?.admin ?? false
  const note = nonCoreNote(items)
  const stale = items.some((item) => item.tiers.some((tier) => tier.failing || tier.throttled))
  return (
    <div className={styles.popAnchor}>
      <button type="button" className={styles.freshBtn} aria-haspopup="dialog" aria-expanded={open} title="Data freshness" onClick={() => setOpen((shown) => !shown)}>
        <StatusDot kind={worstKind(items)}>{`${current} of ${total} regions current`}</StatusDot>
      </button>
      {note && <span className={styles.freshNote}>{note}</span>}
      <Popover open={open} onClose={() => setOpen(false)} label="Data freshness" align="right" width={400}>
        <div className={styles.freshPop}>
          <strong>Data freshness</strong>
          {items.length === 0 && <span className={styles.freshCurrent}>No regions are polled yet.</span>}
          {items.map((item) => (
            <RegionRow key={`${item.account_id}/${item.region}`} item={item} canSeeError={admin} />
          ))}
          {admin && stale && (
            <Link to="/settings/accounts" onClick={() => setOpen(false)}>
              Open AWS accounts
            </Link>
          )}
        </div>
      </Popover>
    </div>
  )
}
