// Pure helpers for the Notifications page: the route draft and its request body, scope keys, validation, and the
// copy for the preview and the delivery log.
import type {
  Destination,
  DeliveryStatus,
  NotificationDelivery,
  NotificationPreview,
  NotificationRoute,
  NotificationRouteInput,
  NotificationSelector,
  NotificationSelectorInput,
  NotificationTargets,
  SelectorKind,
  Severity,
} from '../../../api/notifications'
import type { OffHoursTags } from '../../../api/offhours'
import { parseAddableAwsTag, selectorFromKey, selectorKey } from '../offhours/offhours'

export const DESTINATIONS: { value: Destination; label: string; targetLabel: string }[] = [
  { value: 'slack', label: 'Slack', targetLabel: 'Channel' },
  { value: 'telegram', label: 'Telegram', targetLabel: 'Chat ID' },
  { value: 'pagerduty', label: 'PagerDuty', targetLabel: 'Service' },
  { value: 'webhook', label: 'Webhook', targetLabel: 'URL' },
  { value: 'email', label: 'Email', targetLabel: 'Addresses' },
]

export function destinationLabel(destination: Destination): string {
  return DESTINATIONS.find((d) => d.value === destination)?.label ?? destination
}

export const REPEATS: { value: 0 | 1800 | 7200; label: string }[] = [
  { value: 0, label: 'Do not repeat' },
  { value: 1800, label: 'Every 30 minutes' },
  { value: 7200, label: 'Every 2 hours' },
]

/** The seven routable alert kinds, in display order (the API sends the same list with labels). */
export const KIND_KEYS = [
  'service_failing',
  'service_degraded',
  'restart_loop',
  'unhealthy_task',
  'deployment_failed',
  'cannot_place',
  'account_unreachable',
]

export const MAX_SCOPE_ENTRIES = 100
export const MAX_THRESHOLD = 500

// ---- scope ----

/** 'account:<id>' | 'account:<id>:<region>' | 'cluster:<cluster_id>' | 'service:<service_id>' | the off-hours key of a tag. */
export type ScopeKey = string

type AnySelector = NotificationSelector | NotificationSelectorInput

/** Cluster and account entries are threshold entries; service and tag entries notify per alert. */
export function isGroupKind(kind: SelectorKind): boolean {
  return kind === 'cluster' || kind === 'account'
}

export function scopeKey(selector: AnySelector): ScopeKey {
  switch (selector.kind) {
    case 'account':
      return selector.region ? `account:${selector.account_id}:${selector.region}` : `account:${selector.account_id}`
    case 'cluster':
      return `cluster:${selector.cluster_id}`
    case 'service':
      return `service:${selector.service_id}`
    case 'harbormaster':
      return selectorKey({ kind: 'harbormaster', tag_id: selector.tag_id })
    case 'aws':
      return selectorKey({ kind: 'aws', key: selector.key, value: selector.value })
  }
}

/** The request form of a key; cluster and account entries start at threshold 1. Null for text `scopeKey` did not produce. */
export function selectorFromScopeKey(key: ScopeKey): NotificationSelectorInput | null {
  if (key.startsWith('account:')) {
    const [accountId, region] = key.slice('account:'.length).split(':')
    if (!accountId) return null
    return region ? { kind: 'account', account_id: accountId, region, threshold: 1 } : { kind: 'account', account_id: accountId, threshold: 1 }
  }
  if (key.startsWith('cluster:')) return key.length > 'cluster:'.length ? { kind: 'cluster', cluster_id: key.slice('cluster:'.length), threshold: 1 } : null
  if (key.startsWith('service:')) return key.length > 'service:'.length ? { kind: 'service', service_id: key.slice('service:'.length) } : null
  const tag = selectorFromKey(key)
  if (!tag) return null
  return tag.kind === 'harbormaster' ? { kind: 'harbormaster', tag_id: tag.tag_id } : { kind: 'aws', key: tag.key, value: tag.value }
}

export function scopeLabel(selector: NotificationSelector): string {
  switch (selector.kind) {
    case 'account':
      return selector.region ? `${selector.account_name} / ${selector.region}` : `${selector.account_name} (all regions)`
    case 'cluster':
      return selector.cluster
    case 'service':
      return `${selector.service} · ${selector.cluster}`
    case 'aws':
      return `${selector.key}=${selector.value}`
    case 'harbormaster':
      return selector.tag_name || 'Deleted tag'
  }
}

/** The short label the route list uses: threshold entries carry their number, a service shows its name only. */
function summaryLabel(selector: NotificationSelector): string {
  switch (selector.kind) {
    case 'account':
    case 'cluster':
      return `${scopeLabel(selector)} (${selector.threshold ?? 1}+)`
    case 'service':
      return selector.service
    default:
      return scopeLabel(selector)
  }
}

const SUMMARY_LABELS = 2

/** 'Everything', or the first two entries and how many more. */
export function scopeSummary(route: Pick<NotificationRoute, 'scope'>): string {
  if (route.scope.length === 0) return 'Everything'
  const shown = route.scope.slice(0, SUMMARY_LABELS).map(summaryLabel).join(', ')
  return route.scope.length > SUMMARY_LABELS ? `${shown} +${route.scope.length - SUMMARY_LABELS}` : shown
}

/** 'when any service is failing' | 'when 3 or more services are failing'. */
export function thresholdPhrase(n: number): string {
  return n <= 1 ? 'when any service is failing' : `when ${n} or more services are failing`
}

export type ScopeGroup = 'Accounts' | 'Clusters' | 'Services' | 'Harbormaster tags' | 'AWS tags'
export const SCOPE_GROUPS: ScopeGroup[] = ['Accounts', 'Clusters', 'Services', 'Harbormaster tags', 'AWS tags']

export interface ScopeOption {
  key: ScopeKey
  group: ScopeGroup
  label: string
  /** The account and region (or the tag's service count), shown muted under the label. */
  sub: string
  /** A saved entry whose target no longer exists. */
  missing?: boolean
}

function groupOf(kind: SelectorKind): ScopeGroup {
  switch (kind) {
    case 'account':
      return 'Accounts'
    case 'cluster':
      return 'Clusters'
    case 'service':
      return 'Services'
    case 'harbormaster':
      return 'Harbormaster tags'
    case 'aws':
      return 'AWS tags'
  }
}

/** Every entry the picker can add, grouped in display order, plus saved entries that are no longer offered. */
export function scopeOptions(targets: NotificationTargets, tags: OffHoursTags, saved: NotificationSelector[]): ScopeOption[] {
  const options: ScopeOption[] = []
  for (const account of targets.accounts) {
    options.push({ key: `account:${account.account_id}`, group: 'Accounts', label: `${account.name} (all regions)`, sub: account.account_id })
    for (const region of account.regions) {
      options.push({ key: `account:${account.account_id}:${region}`, group: 'Accounts', label: `${account.name} / ${region}`, sub: account.account_id })
    }
  }
  for (const cluster of targets.clusters) {
    options.push({ key: `cluster:${cluster.id}`, group: 'Clusters', label: cluster.name, sub: `${cluster.account_name} · ${cluster.region}` })
  }
  for (const service of targets.services) {
    options.push({ key: `service:${service.id}`, group: 'Services', label: `${service.name} · ${service.cluster}`, sub: `${service.account_name} · ${service.region}` })
  }
  for (const tag of tags.harbormaster) {
    options.push({ key: selectorKey({ kind: 'harbormaster', tag_id: tag.id }), group: 'Harbormaster tags', label: tag.name, sub: `${tag.services} services` })
  }
  for (const tag of tags.items) {
    options.push({ key: selectorKey({ kind: 'aws', key: tag.key, value: tag.value }), group: 'AWS tags', label: `${tag.key}=${tag.value}`, sub: `${tag.services} services` })
  }
  const known = new Set(options.map((option) => option.key))
  for (const selector of saved) {
    const key = scopeKey(selector)
    if (known.has(key)) continue
    known.add(key)
    // An AWS tag that no service carries today is still a valid entry; a Harbormaster tag is missing when the server
    // says its tag was deleted.
    const missing = selector.kind === 'aws' ? false : selector.kind === 'harbormaster' ? selector.missing : true
    options.push({ key, group: groupOf(selector.kind), label: scopeLabel(selector), sub: '', ...(missing ? { missing: true } : {}) })
  }
  return SCOPE_GROUPS.flatMap((group) => options.filter((option) => option.group === group))
}

/** A typed `key=value` the picker may add as an AWS tag (`aws:` keys are reserved by AWS). */
export function addableAwsTagKey(text: string): ScopeKey | null {
  const parsed = parseAddableAwsTag(text)
  return parsed ? selectorKey({ kind: 'aws', key: parsed.key, value: parsed.value }) : null
}

// ---- the draft ----

export interface ScopeEntry {
  key: ScopeKey
  /** Failing services that make a cluster or account entry notify (1 to 500); 0 for service and tag entries. */
  threshold: number
}

export interface RouteDraft {
  name: string
  enabled: boolean
  destination: Destination
  label: string
  /** Email addresses, separated by commas, semicolons or line breaks. */
  recipients: string
  pagerdutyRegion: 'us' | 'eu'
  chatId: string
  threadId: string
  // Secrets are write-only: they are empty for a saved route and sent only when typed.
  url: string
  routingKey: string
  signingSecret: string
  /** Remove the stored signing secret. */
  clearSigning: boolean
  botToken: string
  minSeverity: Severity
  kinds: string[]
  repeatSeconds: 0 | 1800 | 7200
  quietEnabled: boolean
  quietTimezone: string
  notifyResolved: boolean
  scope: ScopeEntry[]
  fallback: boolean
  investigate: boolean
}

/** "New route N", the first number no existing route uses; Slack, critical only, every alert type, off. */
export function newRouteDraft(existingNames: string[], timezone: string): RouteDraft {
  const taken = new Set(existingNames.map((name) => name.trim().toLowerCase()))
  let n = 1
  while (taken.has(`new route ${n}`)) n++
  return {
    name: `New route ${n}`,
    enabled: false,
    destination: 'slack',
    label: '',
    recipients: '',
    pagerdutyRegion: 'us',
    chatId: '',
    threadId: '',
    url: '',
    routingKey: '',
    signingSecret: '',
    clearSigning: false,
    botToken: '',
    minSeverity: 'critical',
    kinds: [...KIND_KEYS],
    repeatSeconds: 0,
    quietEnabled: false,
    quietTimezone: timezone,
    notifyResolved: true,
    scope: [],
    fallback: false,
    investigate: false,
  }
}

export function draftFromRoute(route: NotificationRoute): RouteDraft {
  return {
    name: route.name,
    enabled: route.enabled,
    destination: route.destination,
    label: route.label,
    recipients: route.recipients.join(', '),
    pagerdutyRegion: route.pagerduty_region === 'eu' ? 'eu' : 'us',
    chatId: route.chat_id,
    threadId: route.thread_id ? String(route.thread_id) : '',
    url: '',
    routingKey: '',
    signingSecret: '',
    clearSigning: false,
    botToken: '',
    minSeverity: route.min_severity,
    kinds: [...route.kinds],
    repeatSeconds: route.repeat_seconds === 1800 || route.repeat_seconds === 7200 ? route.repeat_seconds : 0,
    quietEnabled: route.quiet_enabled,
    quietTimezone: route.quiet_timezone,
    notifyResolved: route.notify_resolved,
    scope: route.scope.map((entry) => ({ key: scopeKey(entry), threshold: isGroupKind(entry.kind) ? (entry.threshold ?? 1) : 0 })),
    fallback: route.fallback,
    investigate: route.investigate,
  }
}

function entryKind(key: ScopeKey): SelectorKind | null {
  return selectorFromScopeKey(key)?.kind ?? null
}

/** Adds an entry; a cluster or account starts at threshold 1. An entry already picked leaves the draft as it was. */
export function addScopeEntry(draft: RouteDraft, key: ScopeKey): RouteDraft {
  if (draft.scope.some((entry) => entry.key === key) || draft.scope.length >= MAX_SCOPE_ENTRIES) return draft
  const kind = entryKind(key)
  if (!kind) return draft
  return { ...draft, scope: [...draft.scope, { key, threshold: isGroupKind(kind) ? 1 : 0 }] }
}

export function removeScopeEntry(draft: RouteDraft, key: ScopeKey): RouteDraft {
  return { ...draft, scope: draft.scope.filter((entry) => entry.key !== key) }
}

/** Sets the threshold of a cluster or account entry, rounded and clamped to 1..500. Other entries are unchanged. */
export function setThreshold(draft: RouteDraft, key: ScopeKey, value: number): RouteDraft {
  const rounded = Number.isFinite(value) ? Math.round(value) : 1
  const clamped = Math.min(MAX_THRESHOLD, Math.max(1, rounded))
  return {
    ...draft,
    scope: draft.scope.map((entry) => (entry.key === key && entry.threshold > 0 ? { ...entry, threshold: clamped } : entry)),
  }
}

function splitRecipients(text: string): string[] {
  return text
    .split(/[,;\n]/)
    .map((address) => address.trim())
    .filter(Boolean)
}

function parseThreadId(text: string): number | null {
  const n = Number(text.trim())
  return text.trim() !== '' && Number.isInteger(n) && n > 0 ? n : null
}

function scopeInputs(entries: ScopeEntry[]): NotificationSelectorInput[] {
  const out: NotificationSelectorInput[] = []
  for (const entry of entries) {
    const selector = selectorFromScopeKey(entry.key)
    if (!selector) continue
    out.push(selector.kind === 'account' || selector.kind === 'cluster' ? { ...selector, threshold: entry.threshold || 1 } : selector)
  }
  return out
}

/** The request body. A secret that was not retyped is left out, so the stored one is kept. */
export function bodyFromDraft(draft: RouteDraft, saved?: NotificationRoute): NotificationRouteInput {
  const destination = draft.destination
  const body: NotificationRouteInput = {
    name: draft.name.trim(),
    enabled: draft.enabled,
    destination,
    label: destination === 'slack' || destination === 'pagerduty' || destination === 'telegram' ? draft.label.trim() : '',
    recipients: destination === 'email' ? splitRecipients(draft.recipients) : [],
    pagerduty_region: destination === 'pagerduty' ? draft.pagerdutyRegion : '',
    chat_id: destination === 'telegram' ? draft.chatId.trim() : '',
    thread_id: destination === 'telegram' ? parseThreadId(draft.threadId) : null,
    min_severity: draft.minSeverity,
    kinds: [...draft.kinds],
    repeat_seconds: draft.repeatSeconds,
    quiet_enabled: draft.quietEnabled,
    quiet_timezone: draft.quietTimezone,
    notify_resolved: draft.notifyResolved,
    scope: scopeInputs(draft.scope),
    fallback: draft.fallback,
  }
  if ((destination === 'slack' || destination === 'webhook') && draft.url.trim()) body.url = draft.url.trim()
  if (destination === 'pagerduty' && draft.routingKey.trim()) body.routing_key = draft.routingKey.trim()
  if (destination === 'telegram' && draft.botToken.trim()) body.bot_token = draft.botToken.trim()
  if (destination === 'webhook') {
    if (draft.clearSigning) body.signing_secret = ''
    else if (draft.signingSecret) body.signing_secret = draft.signingSecret
  }
  // Left out unless changed, so a save never sends the switch the server would refuse while the agent is off.
  if (draft.investigate !== (saved?.investigate ?? false)) body.investigate = draft.investigate
  if (saved) body.version = saved.version
  return body
}

/** True for a route that is not saved yet, and for a saved one that differs from the draft or has a retyped secret. */
export function isDirty(draft: RouteDraft, saved?: NotificationRoute): boolean {
  if (!saved) return true
  return JSON.stringify(bodyFromDraft(draft, saved)) !== JSON.stringify(bodyFromDraft(draftFromRoute(saved), saved))
}

const CHAT_ID = /^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$/
const BOT_TOKEN = /^[0-9]{6,12}:[A-Za-z0-9_-]{30,64}$/

export interface Capabilities {
  emailAvailable: boolean
  secretsAvailable: boolean
}

/** The first problem with the draft as a sentence, or null when it can be saved. The server checks the rest. */
export function validateDraft(draft: RouteDraft, saved: NotificationRoute | undefined, caps: Capabilities): string | null {
  if (!draft.name.trim()) return 'Enter a route name.'
  const destination = draft.destination
  if (destination !== 'email' && !caps.secretsAvailable) return 'Set HM_SESSION_KEY before adding notification routes.'
  if (destination === 'email' && !caps.emailAvailable) return 'Set HM_SMTP_HOST to send email.'
  if (draft.kinds.length === 0) return 'Choose at least one alert type.'

  // The destination's secret is needed for a new route, after a change of destination, and when the stored one
  // no longer opens.
  const needsSecret = !saved || saved.destination !== destination || saved.secret_unreadable
  const missingSecret = (message: string) => (saved?.secret_unreadable && saved.destination === destination ? 'Enter the destination again.' : message)
  switch (destination) {
    case 'slack':
      if (needsSecret && !draft.url.trim()) return missingSecret('Enter a Slack incoming webhook URL (https://hooks.slack.com/...).')
      break
    case 'webhook':
      if (needsSecret && !draft.url.trim()) return missingSecret('Enter an https URL.')
      break
    case 'pagerduty':
      if (needsSecret && !draft.routingKey.trim()) return missingSecret('Enter the 32-character integration key from PagerDuty.')
      break
    case 'telegram': {
      const token = draft.botToken.trim()
      if ((needsSecret && !token) || (token && !BOT_TOKEN.test(token))) return missingSecret('Enter the bot token BotFather gave you.')
      if (!CHAT_ID.test(draft.chatId.trim())) return 'Enter a numeric chat ID or @channelname.'
      if (draft.threadId.trim() && parseThreadId(draft.threadId) === null) return 'Enter a topic ID, or leave it empty.'
      break
    }
    case 'email':
      if (splitRecipients(draft.recipients).length === 0) return 'Enter at least one email address.'
      break
  }
  if (draft.scope.some((entry) => entry.threshold !== 0 && (entry.threshold < 1 || entry.threshold > MAX_THRESHOLD))) {
    return 'A threshold must be between 1 and 500.'
  }
  return null
}

// ---- preview ----

const noun = (n: number, one: string, many: string) => (n === 1 ? one : many)

export function previewSentence(preview: NotificationPreview): { text: string; warn: boolean } {
  const n = preview.notification_count
  if (n === 0) return { text: 'This route matches no open alerts. Check the severity, scope and alert types.', warn: true }
  return { text: `Right now this route would send ${n} ${noun(n, 'notification', 'notifications')}.`, warn: false }
}

const MAX_PREVIEW_NAMES = 5

export interface PreviewLine {
  text: string
  /** An entry below its threshold: nothing is sent for it. */
  muted: boolean
}

/** Open groups, then per-alert names (deduplicated, five and "and N more"), then entries below their threshold. */
export function previewLineItems(preview: NotificationPreview): PreviewLine[] {
  const lines: PreviewLine[] = []
  for (const group of preview.groups.filter((g) => g.open)) {
    lines.push({ text: group.unreachable ? `${group.label}: unreachable` : `${group.label}: ${group.failing} of ${group.total} services failing`, muted: false })
  }
  const names = [...new Set(preview.matches.map((match) => match.title))]
  lines.push(...names.slice(0, MAX_PREVIEW_NAMES).map((text) => ({ text, muted: false })))
  if (names.length > MAX_PREVIEW_NAMES) lines.push({ text: `and ${names.length - MAX_PREVIEW_NAMES} more`, muted: false })
  for (const group of preview.groups.filter((g) => !g.open)) {
    lines.push({ text: `${group.label}: ${group.failing} of ${group.total} services failing, below ${group.threshold}. Nothing is sent.`, muted: true })
  }
  return lines
}

const minutesOf = (seconds: number) => Math.max(1, Math.ceil(seconds / 60))
const minutesText = (seconds: number) => {
  const minutes = minutesOf(seconds)
  return `${minutes} ${noun(minutes, 'minute', 'minutes')}`
}

/** Says that scaling is not a failure and how long a capacity alert must last; empty when the wait is off. */
export function settleSentence(seconds: number): string {
  if (seconds <= 0) return ''
  return `Scaling up or down is not a failure and is never notified. A service with no running tasks, or fewer than it should have, is reported once it has stayed that way for ${minutesText(seconds)}.`
}

export function pendingSentence(count: number, seconds: number): string {
  return `${count} more may follow once they have lasted ${minutesText(seconds)}.`
}

// ---- delivery log ----

/** A diagnosis waits for its due time as a pending row; until then it reads as held. */
export function isHeldPending(delivery: Pick<NotificationDelivery, 'status' | 'held_until'>, now = Date.now()): boolean {
  return delivery.status === 'pending' && delivery.held_until !== null && new Date(delivery.held_until).getTime() > now
}

export function statusPill(delivery: Pick<NotificationDelivery, 'status' | 'held_until'>): { kind: 'ok' | 'bad' | 'warn' | 'mute'; label: string } {
  if (isHeldPending(delivery)) return { kind: 'warn', label: 'Held' }
  const pills: Record<DeliveryStatus, { kind: 'ok' | 'bad' | 'warn' | 'mute'; label: string }> = {
    delivered: { kind: 'ok', label: 'Delivered' },
    failed: { kind: 'bad', label: 'Failed' },
    held: { kind: 'warn', label: 'Held' },
    pending: { kind: 'mute', label: 'Pending' },
    cancelled: { kind: 'mute', label: 'Not sent' },
  }
  return pills[delivery.status]
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** The note under the result: the release time of a held row in the browser's zone, else what the API says. */
export function deliveryNote(delivery: Pick<NotificationDelivery, 'status' | 'held_until' | 'note'>): string {
  if (isHeldPending(delivery)) {
    const at = new Date(delivery.held_until as string)
    return `Held until ${pad2(at.getHours())}:${pad2(at.getMinutes())}`
  }
  if (delivery.status === 'held' && delivery.held_until) {
    const at = new Date(delivery.held_until)
    return `Held by quiet hours until ${pad2(at.getHours())}:${pad2(at.getMinutes())}`
  }
  return delivery.note
}

export function failedBanner(n: number): string {
  return n === 1 ? '1 delivery failed in the last 24 hours.' : `${n} deliveries failed in the last 24 hours.`
}

export function eventLabel(event: NotificationDelivery['event']): string {
  const labels: Record<NotificationDelivery['event'], string> = {
    opened: 'Opened',
    repeat: 'Repeat',
    update: 'Updated',
    resolved: 'Resolved',
    test: 'Test',
    diagnosis: 'Diagnosis',
  }
  return labels[event]
}
