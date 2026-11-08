import type { SecurityDB } from '../../api/security'
import { formatAgo } from '../../components/Ago'
import type { PillKind } from '../../components/Pill'
import { SECURITY_COPY } from './copy'

export interface DbBadge {
  kind: PillKind
  text: string
}

/** The badge for the vulnerability database: a running refresh wins over the band, and no build date means no data yet. */
export function dbBadge(db: SecurityDB, now: number): DbBadge {
  if (db.refreshing) return { kind: 'info', text: SECURITY_COPY.dbUpdating }
  if (db.built_at === null || db.freshness === '' || db.freshness === 'none') return { kind: 'mute', text: SECURITY_COPY.dbNone }
  const kind: PillKind = db.freshness === 'fresh' ? 'ok' : db.freshness === 'warn' ? 'warn' : 'bad'
  return { kind, text: SECURITY_COPY.dbBuilt(formatAgo(db.built_at, now)) }
}

/** "30 Nov 2026", in UTC so the date does not depend on the viewer's time zone. */
export const formatDate = (iso: string | number): string =>
  new Date(iso).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })

/** "31 Oct 2026, 08:00 UTC". */
export const formatDateTime = (iso: string): string =>
  `${new Date(iso).toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' })} UTC`
