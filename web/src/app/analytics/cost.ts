import type { CostRightsizing, CostService, CostSpot, CostSpotService, CostStep, CostSuggestion } from '../../api/analytics'

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

/** The title of a draft dialog: "Draft 2 change sets". */
export const draftTitle = (count: number) => `Draft ${count} change ${count === 1 ? 'set' : 'sets'}`

/** Services the server says can move to Fargate Spot, in the order it ranked them (by saving). */
export const spotCandidates = (spot: CostSpot): CostSpotService[] => spot.services.filter((service) => service.status === 'candidate')

/**
 * A candidate is ticked unless it carries a caution (terminal sessions or killed stops), and never without Deploy. `toggled`
 * holds the ids the user flipped from that default, so a refetch that changes a caution does not undo a choice.
 */
export function isSpotTicked(service: CostSpotService, toggled: ReadonlySet<string>): boolean {
  if (!service.can_deploy) return false
  return !service.caution !== toggled.has(service.service_id)
}

export const selectedSpot = (spot: CostSpot, toggled: ReadonlySet<string>): CostSpotService[] =>
  spotCandidates(spot).filter((service) => isSpotTicked(service, toggled))

/** The sentences under "Why it qualifies": what the server returned, worded. */
export function spotReasons(service: CostSpotService, lookbackDays: number): { text: string; title?: string }[] {
  const days = `${lookbackDays} days`
  const reasons: { text: string; title?: string }[] = [
    { text: `Non-production tag ${service.tag.name}` },
    { text: `${service.desired_tasks} tasks; ${service.on_demand_base} stays on-demand` },
    { text: service.terminal_sessions === 0 ? `No terminal sessions in ${days}` : `${service.terminal_sessions} terminal sessions in ${days}` },
  ]
  const timeout =
    service.stop_timeout_seconds === null ? undefined : `Container stop timeout ${service.stop_timeout_seconds} s. It cannot show that the process handles SIGTERM.`
  if (service.stop_evidence === 'graceful') reasons.push({ text: `Stops cleanly on SIGTERM (${service.expected_stops} stops in ${days})`, title: timeout })
  else if (service.stop_evidence === 'killed') reasons.push({ text: `${service.killed_stops} of ${service.expected_stops} stops in ${days} were killed (exit 137)`, title: timeout })
  else reasons.push({ text: `No deployment or scale-in stops in ${days} to judge SIGTERM handling`, title: timeout })
  return reasons
}

/** Why a service is not offered for Fargate Spot. */
export function spotNotCandidateReason(service: CostSpotService, minTasks: number): string {
  switch (service.status) {
    case 'production':
      return `Production tag ${service.tag.name}`
    case 'single_task':
      return `Runs 1 task; Spot needs at least ${minTasks}`
    case 'already_spot':
      return 'Already uses Fargate Spot'
    case 'no_cluster_spot':
      return `Cluster ${service.cluster} has no FARGATE_SPOT capacity provider. Associate it with the cluster (in Terraform: aws_ecs_cluster_capacity_providers), then this service qualifies.`
    case 'not_fargate':
      return 'Not on Fargate'
    case 'windows':
      return 'Windows tasks cannot run on Fargate Spot'
    case 'no_price':
      return 'No Spot price for this region'
    case 'not_running':
      return 'No running on-demand tasks'
    case 'custom_capacity':
      return 'Uses a capacity provider Harbormaster does not edit'
    case 'unsupported_controller':
      return `Deployment controller ${service.controller_name} is not supported`
    default:
      return 'Not a candidate'
  }
}
