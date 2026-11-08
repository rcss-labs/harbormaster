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

/** Adds up the suggestions the server returned at one headroom. Spot tasks are priced at the on-demand list, so their saving is an upper bound. */
export function costSummary(rows: CostService[], headroomPct: number): { saving: number; services: number; upperBound: boolean } {
  let saving = 0
  let services = 0
  let upperBound = false
  for (const row of rows) {
    const suggestion = stepAt(row.steps, headroomPct)
    if (suggestion === null) continue
    saving += suggestion.saving_monthly
    services += 1
    if (row.spot_tasks > 0) upperBound = true
  }
  return { saving, services, upperBound }
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
