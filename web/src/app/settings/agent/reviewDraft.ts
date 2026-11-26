import type { AgentReviewsInfo, ReviewCadence, ReviewDTO, ReviewInput, ReviewScopeEntry } from '../../../api/agent'
import { scopeKey, selectorFromScopeKey, type ScopeGroup } from '../notifications/notifications'
import { formatMinute, normalizeSchedule, parseTime } from './schedule'

export const MAX_REVIEW_ROUTES = 5
export const MAX_REVIEW_SCOPE_ENTRIES = 10
export const MAX_REVIEW_NAME = 80
export const REVIEW_SCOPE_GROUPS: readonly ScopeGroup[] = ['Accounts', 'Clusters', 'Harbormaster tags']

/** One line per type saying what it reads. A type this version does not know has none. */
export const TYPE_LINES: Record<string, string> = {
  cost: 'Reads spend, cost trends and idle capacity.',
  security: 'Reads open security findings and image scan results.',
  reliability: 'Reads incidents, failing deployments and restarts.',
  capacity: 'Reads CPU and memory use against what is provisioned.',
  custom: 'You write what it looks at.',
}

export const CUSTOM_TYPE = 'custom'
const DEFAULT_MAX_INSTRUCTION_CHARS = 2000

/** The text as the server cleans it before it counts: line breaks become line feeds and white space at both ends goes. */
const cleanedInstructions = (text: string) => text.replace(/\r\n?/g, '\n').trim()

/** Characters as the server counts them: code points, not UTF-16 units. */
export const instructionLength = (text: string): number => Array.from(cleanedInstructions(text)).length

/** The problem code of a draft's instructions, or '' when they pass the checks the page can make or the type is not custom. */
export function draftInstructionProblem(draft: ReviewDraft, info: AgentReviewsInfo): string {
  if (draft.type !== CUSTOM_TYPE) return ''
  const max = info.max_instruction_chars || DEFAULT_MAX_INSTRUCTION_CHARS
  const length = instructionLength(draft.instructions)
  if (length === 0) return 'instructions_required'
  return length > max ? 'instructions_too_long' : ''
}

export interface ReviewDraft {
  name: string
  type: string
  enabled: boolean
  cadence: ReviewCadence
  /** HH:MM as the time field holds it; it can be empty on the way to a new value. */
  time: string
  /** Set only while the cadence is weekly. */
  weekday: number | null
  /** What is typed in the day field; used only while the cadence is monthly. */
  monthDay: string
  timezone: string
  /** Scope keys of the notification scope picker. */
  scope: string[]
  routeIds: number[]
  /** Used only while the type is custom. */
  instructions: string
}

/** "New review N", the first number no review uses; weekly on Monday at 08:00, off, nothing in scope. */
export function newReviewDraft(existingNames: string[], timezone: string, type: string): ReviewDraft {
  const taken = new Set(existingNames.map((name) => name.trim().toLowerCase()))
  let n = 1
  while (taken.has(`new review ${n}`)) n++
  return { name: `New review ${n}`, type, enabled: false, cadence: 'weekly', time: '08:00', weekday: 1, monthDay: '', timezone, scope: [], routeIds: [], instructions: '' }
}

export function draftFromReview(review: ReviewDTO): ReviewDraft {
  const { schedule } = review
  return {
    name: review.name,
    type: review.type,
    enabled: review.enabled,
    cadence: schedule.cadence,
    time: formatMinute(schedule.at_minute),
    weekday: schedule.weekday,
    monthDay: schedule.month_day === null ? '' : String(schedule.month_day),
    timezone: schedule.timezone,
    scope: review.scope.map(scopeKey),
    // A route that became PagerDuty is not offered any more; the server keeps it when only the switch is turned off.
    routeIds: review.routes.filter((route) => route.destination !== 'pagerduty').map((route) => route.id),
    instructions: review.instructions ?? '',
  }
}

/** The weekday and the day of the month belong to one cadence each; switching clears the other and starts the new one at 1. */
export function withCadence(draft: ReviewDraft, cadence: ReviewCadence): ReviewDraft {
  return {
    ...draft,
    cadence,
    weekday: cadence === 'weekly' ? (draft.weekday ?? 1) : null,
    monthDay: cadence === 'monthly' ? (draft.monthDay || '1') : '',
  }
}

/** A review's scope is a focus, not a count, so entries carry no threshold. Only tags, clusters and accounts are scope. */
function scopeInput(key: string): ReviewScopeEntry | null {
  const selector = selectorFromScopeKey(key)
  if (!selector) return null
  if (selector.kind === 'account') return selector.region ? { kind: 'account', account_id: selector.account_id, region: selector.region } : { kind: 'account', account_id: selector.account_id }
  if (selector.kind === 'cluster') return { kind: 'cluster', cluster_id: selector.cluster_id }
  if (selector.kind === 'harbormaster') return { kind: 'harbormaster', tag_id: selector.tag_id }
  return null
}

/** The request body. The weekday and the day of the month are sent only for the cadence they belong to. */
export function bodyFromDraft(draft: ReviewDraft, version?: number): ReviewInput {
  const day = Number(draft.monthDay)
  const body: ReviewInput = {
    name: draft.name.trim(),
    type: draft.type,
    enabled: draft.enabled,
    schedule: normalizeSchedule({
      cadence: draft.cadence,
      at_minute: parseTime(draft.time) ?? 0,
      weekday: draft.weekday,
      month_day: Number.isInteger(day) && day > 0 ? day : null,
      timezone: draft.timezone,
    }),
    scope: draft.scope.map(scopeInput).filter((entry): entry is ReviewScopeEntry => entry !== null),
    route_ids: draft.routeIds,
  }
  // The text goes as typed (the server cleans it) and only with the custom type.
  if (draft.type === CUSTOM_TYPE) body.instructions = draft.instructions
  if (version !== undefined) body.version = version
  return body
}

/** A saved review as a request with `enabled` set; the switch in the list saves it this way. */
export function inputFromReview(review: ReviewDTO, enabled: boolean): ReviewInput {
  return bodyFromDraft({ ...draftFromReview(review), enabled }, review.version)
}

export function isDirty(draft: ReviewDraft, saved: ReviewDTO | undefined): boolean {
  if (!saved) return true
  return JSON.stringify(bodyFromDraft(draft)) !== JSON.stringify(bodyFromDraft(draftFromReview(saved)))
}

/** The first problem with the draft, or null. `saved` is the stored review being edited. Names are compared without regard to case, as the server does. */
export function validateDraft(draft: ReviewDraft, info: AgentReviewsInfo, otherNames: string[], saved?: ReviewDTO): string | null {
  const name = draft.name.trim()
  if (name === '') return 'Enter a name.'
  if (name.length > MAX_REVIEW_NAME) return `The name can be at most ${MAX_REVIEW_NAME} characters.`
  if (otherNames.some((other) => other.trim().toLowerCase() === name.toLowerCase())) return 'Another review already has this name.'
  if (!info.types.some((type) => type.id === draft.type)) return 'Choose a type.'
  if (parseTime(draft.time) === null) return 'Enter a time of day.'
  if (draft.cadence === 'weekly' && !(draft.weekday !== null && draft.weekday >= 1 && draft.weekday <= 7)) return 'Choose a weekday.'
  if (draft.cadence === 'monthly') {
    const day = Number(draft.monthDay)
    if (draft.monthDay.trim() === '' || !Number.isInteger(day) || day < 1 || day > 28) return 'Choose a day from 1 to 28.'
  }
  if (draft.timezone === '') return 'Choose a time zone.'
  // An enabled review can always be turned off, whatever became of its routes.
  const turningOff = saved?.enabled === true && !draft.enabled
  if (draft.routeIds.length === 0 && !turningOff) return 'Choose at least one route.'
  if (draft.routeIds.length > MAX_REVIEW_ROUTES) return `Choose at most ${MAX_REVIEW_ROUTES} routes.`
  return null
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

/**
 * After a conflict: what the admin changed since `baseline` stays as typed, every other field follows `stored`.
 * The cadence, weekday and day of the month move together because each depends on the others.
 */
export function mergeDraft(baseline: ReviewDraft, mine: ReviewDraft, stored: ReviewDraft): ReviewDraft {
  const pick = <K extends keyof ReviewDraft>(key: K): ReviewDraft[K] => (same(mine[key], baseline[key]) ? stored[key] : mine[key])
  const scheduleTouched = !same([mine.cadence, mine.weekday, mine.monthDay], [baseline.cadence, baseline.weekday, baseline.monthDay])
  const schedule = scheduleTouched ? mine : stored
  return {
    name: pick('name'),
    type: pick('type'),
    enabled: pick('enabled'),
    cadence: schedule.cadence,
    weekday: schedule.weekday,
    monthDay: schedule.monthDay,
    time: pick('time'),
    timezone: pick('timezone'),
    scope: pick('scope'),
    routeIds: pick('routeIds'),
    instructions: pick('instructions'),
  }
}
