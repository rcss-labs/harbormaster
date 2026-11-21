// Pure helpers for the Config search page: query text, labels, CSV export and the selection hand-off type.
import type { ConfigMatchDTO, ConfigSearchItemDTO, ConfigSearchSelection } from '../../api/configSearch'
import type { Filters } from '../../api/hooks'

export const FIELDS: { field: string; label: string; example: string }[] = [
  { field: 'image', label: 'Image', example: 'image:node:18' },
  { field: 'tag', label: 'Image tag', example: 'tag:2.3.0' },
  { field: 'base', label: 'Base image', example: 'base:node:18' },
  { field: 'env', label: 'Environment', example: 'env:DATABASE_URL' },
  { field: 'secret', label: 'Secret', example: 'secret:API_KEY' },
  { field: 'port', label: 'Port', example: 'port:8080' },
  { field: 'role', label: 'Any role', example: 'role:payments' },
  { field: 'taskrole', label: 'Task role', example: 'taskrole:payments' },
  { field: 'execrole', label: 'Execution role', example: 'execrole:ecs-task-execution' },
  { field: 'loggroup', label: 'Log group', example: 'loggroup:/ecs/core' },
]

/** The query lives in the URL as ?q= so a shared link shows results. */
export function queryFromParams(p: URLSearchParams): string {
  return p.get('q') ?? ''
}

/** Adds ` field:` to the query for a chip, unless the query already ends with it. */
export function appendField(query: string, field: string): string {
  const trimmed = query.trimEnd()
  const token = `${field}:`
  if (trimmed === token || trimmed.endsWith(` ${token}`)) return trimmed
  return trimmed === '' ? token : `${trimmed} ${token}`
}

export function resultCountLabel(total: number): string {
  return total === 1 ? '1 service matches' : `${total} services match`
}

/** `env DATABASE_URL`, or `port 8080 (api)` when the match is in a container other than the first. */
export function matchLabel(m: ConfigMatchDTO, firstContainer = ''): string {
  const base = `${m.field} ${m.value}`
  return m.container !== '' && firstContainer !== '' && m.container !== firstContainer ? `${base} (${m.container})` : base
}

const CSV_HEADER = [
  'service', 'cluster', 'account_id', 'account_name', 'region', 'permission_tag', 'family', 'revision',
  'matched_revision', 'previous_revision', 'container', 'image', 'image_tag', 'base_image', 'ports',
  'env_names', 'secret_names', 'task_role', 'execution_role', 'log_group', 'matched', 'service_id',
]

/** A leading = + - @ is neutralised with an apostrophe so a spreadsheet never runs it as a formula. */
function guard(text: string): string {
  return /^[=+\-@]/.test(text) ? `'${text}` : text
}

/** Guards each element of a list cell, so a name like =cmd is safe wherever it sits in the cell. */
const list = (values: string[]): string => values.map(guard).join(' ')

/** One CSV cell: guarded, then quoted when it holds a comma, quote, CR or LF. */
function csvCell(value: string | number): string {
  const text = guard(String(value))
  return /[",\r\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}

/** One line per container (a service with none still gets one line); list cells are joined with a space. CRLF line ends. */
export function toCSV(items: ConfigSearchItemDTO[]): string {
  const lines = [CSV_HEADER.join(',')]
  for (const item of items) {
    const containers = item.containers.length > 0 ? item.containers : [undefined]
    for (const c of containers) {
      const matched = item.matches.filter((m) => c === undefined || m.container === '' || m.container === c.name).map((m) => `${m.field}:${m.value}`)
      const cells = [
        item.name, item.cluster.name, item.account_id, item.account_name, item.region, item.tag.name, item.family,
        item.revision, item.matched_revision, item.previous ? 'yes' : 'no',
        c?.name ?? '', c?.image ?? '', c?.tag ?? '', c?.base_image ?? '', (c?.ports ?? []).join(' '),
        list(c?.env_names ?? []), list(c?.secret_names ?? []), item.task_role, item.execution_role,
        c?.log_group ?? '', list(matched), item.id,
      ]
      lines.push(cells.map(csvCell).join(','))
    }
  }
  return lines.join('\r\n') + '\r\n'
}

/** harbormaster-config-search-YYYYMMDD-HHmm.csv, in local time. */
export function csvFilename(now: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  const date = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}`
  return `harbormaster-config-search-${date}-${pad(now.getHours())}${pad(now.getMinutes())}.csv`
}

/** The single place a selection is assembled, so Bulk changes can import it. */
export function selectionOf(query: string, filters: Filters, ids: string[]): ConfigSearchSelection {
  return { query, filters, serviceIds: ids }
}
