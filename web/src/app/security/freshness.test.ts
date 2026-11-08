import { describe, expect, it } from 'vitest'
import type { SecurityDB } from '../../api/security'
import { dbBadge } from './freshness'

const NOW = Date.parse('2026-11-01T12:00:00Z')
const db = (overrides: Partial<SecurityDB>): SecurityDB => ({
  built_at: '2026-11-01T06:00:00Z',
  freshness: 'fresh',
  refreshing: false,
  last_refresh_error: '',
  ...overrides,
})

describe('freshness bands', () => {
  it('maps the data bands to badge kinds and words', () => {
    expect(dbBadge(db({}), NOW)).toEqual({ kind: 'ok', text: 'Vulnerability data built 6 h ago' })
    expect(dbBadge(db({ freshness: 'warn' }), NOW)).toEqual({ kind: 'warn', text: 'Vulnerability data built 6 h ago' })
    expect(dbBadge(db({ freshness: 'stale' }), NOW)).toEqual({ kind: 'bad', text: 'Vulnerability data built 6 h ago' })
    expect(dbBadge(db({ built_at: null, freshness: 'none' }), NOW)).toEqual({ kind: 'mute', text: 'No vulnerability data yet' })
    expect(dbBadge(db({ built_at: null, freshness: '' }), NOW)).toEqual({ kind: 'mute', text: 'No vulnerability data yet' })
  })

  it('says the data is updating while a refresh runs, whatever the band', () => {
    expect(dbBadge(db({ refreshing: true, freshness: 'stale' }), NOW)).toEqual({ kind: 'info', text: 'Updating vulnerability data' })
  })
})
