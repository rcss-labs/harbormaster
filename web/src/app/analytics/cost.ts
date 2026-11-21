import type { CostRightsizing, CostService, CostStep, CostSuggestion } from '../../api/analytics'

/** Whole US dollars. */
export const money = (value: number) => `$${Math.round(value).toLocaleString('en-US')}`

/** The server sends one step per run of equal suggestions; the one in force is the last that starts at or below the headroom. */
export function stepAt(steps: CostStep[], headroomPct: number): CostSuggestion | null {
  let suggestion: CostSuggestion | null = null
  for (const step of steps) {
    if (step.from_headroom_pct > headroomPct) break
    suggestion = step.suggestion
  }
  return suggestion
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** `2026-10-19` as `19 Oct 2026`; anything else is shown as sent. */
export function priceDate(date: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
  if (!match) return date
  const month = MONTHS[Number(match[2]) - 1]
  return month ? `${Number(match[3])} ${month} ${match[1]}` : date
}

/** The Cost tab banner. Both dates come from the API: the list table and the Spot table are refreshed separately. */
export function costNotice(listDate: string, spotDate: string): string {
  return `Estimate at list prices as of ${priceDate(listDate)}. Spot tasks use Spot prices as of ${priceDate(spotDate)}; Spot prices change. Excludes load balancers, data transfer and storage above 20 GB.`
}

/** Adds up the suggestions the server returned at one headroom. A saving that includes Spot tasks is an estimate at Spot prices, so it reads "about". */
export function costSummary(rows: CostService[], headroomPct: number): { saving: number; services: number; includesSpot: boolean } {
  let saving = 0
  let services = 0
  let includesSpot = false
  for (const row of rows) {
    const suggestion = stepAt(row.steps, headroomPct)
    if (suggestion === null) continue
    saving += suggestion.saving_monthly
    services += 1
    if (row.spot_tasks > 0) includesSpot = true
  }
  return { saving, services, includesSpot }
}

/** A service row with the suggestion in force at the chosen headroom. */
export interface SuggestedService {
  service: CostService
  suggestion: CostSuggestion
}

/** Services with a suggestion at this headroom, in the order the server sent them. */
export function suggestedServices(rightsizing: CostRightsizing, headroomPct: number): SuggestedService[] {
  const rows: SuggestedService[] = []
  for (const service of rightsizing.services) {
    if (service.status !== 'ok') continue
    const suggestion = stepAt(service.steps, headroomPct)
    if (suggestion !== null) rows.push({ service, suggestion })
  }
  return rows
}

/** The suggested services the user can edit and has not unticked. */
export function selectedSuggestions(rightsizing: CostRightsizing, headroomPct: number, excluded: ReadonlySet<string>): SuggestedService[] {
  return suggestedServices(rightsizing, headroomPct).filter(({ service }) => service.can_edit && !excluded.has(service.service_id))
}
