import { http, HttpResponse } from 'msw'
import type { FocusDTO } from '../api/focus'
import { FIXTURE_NOW } from './fixtures'

/** A page that is being read every 5 s, last read at FIXTURE_NOW. */
export const FOCUS_LIVE: FocusDTO = { state: 'live', interval_seconds: 5, refreshed_at: FIXTURE_NOW, message: '', failed: false }

/** A page over the live cap: it stays at the normal pace and says so. */
export const FOCUS_CAPPED: FocusDTO = {
  state: 'capped',
  interval_seconds: 30,
  refreshed_at: null,
  message: 'Normal pace (live limit reached)',
  failed: false,
}

/** Default handlers: every page that asks for live refresh gets it. Override per test with `server.use`. */
export const focusHandlers = [
  http.post('/api/focus', () => HttpResponse.json(FOCUS_LIVE)),
  http.delete('/api/focus', () => new HttpResponse(null, { status: 204 })),
]
