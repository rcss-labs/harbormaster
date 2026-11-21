import { describe, expect, it } from 'vitest'
import type { NotificationPreview, NotificationRoute } from '../../../api/notifications'
import {
  CORE_SERVICES_CLUSTER,
  DELIVERY_CANCELLED,
  DELIVERY_DELIVERED,
  DELIVERY_FAILED,
  DELIVERY_HELD,
  DELIVERY_PENDING,
  DELIVERY_UPDATE,
  IMAGE_RESIZER_SERVICE,
  NOTIFICATION_PREVIEW,
  NOTIFICATION_PREVIEW_EMPTY,
  NOTIFICATION_PREVIEW_GROUPS,
  NOTIFICATION_TARGETS,
  ROUTE_CATCH_ALL,
  ROUTE_CORE_SLACK,
  ROUTE_RESIZER_TELEGRAM,
  ROUTE_STAGING_EMAIL,
  ROUTE_WEBHOOK_SIGNED,
  selector,
} from '../../../test/notificationsFixtures'
import { OFFHOURS_TAGS } from '../../../test/offhoursFixtures'
import {
  addScopeEntry,
  bodyFromDraft,
  deliveryNote,
  draftFromRoute,
  eventLabel,
  failedBanner,
  isDirty,
  isGroupKind,
  newRouteDraft,
  pendingSentence,
  previewLines,
  previewSentence,
  scopeKey,
  scopeLabel,
  scopeOptions,
  scopeSummary,
  selectorFromScopeKey,
  setThreshold,
  settleSentence,
  statusPill,
  thresholdPhrase,
  validateDraft,
} from './notifications'

const CAPS = { emailAvailable: true, secretsAvailable: true }
const TOKEN = '123456789:' + 'Ab-_'.repeat(9)

describe('scope keys', () => {
  it('round-trips every selector kind through its scope key', () => {
    const entries = [
      selector({ kind: 'account', account_id: '482109375512', threshold: 2 }),
      selector({ kind: 'account', account_id: '482109375512', region: 'eu-west-1', threshold: 1 }),
      CORE_SERVICES_CLUSTER,
      IMAGE_RESIZER_SERVICE,
      selector({ kind: 'harbormaster', tag_id: 7 }),
      selector({ kind: 'aws', key: 'env', value: 'staging' }),
      selector({ kind: 'aws', key: 'empty', value: '' }),
    ]
    for (const entry of entries) {
      const key = scopeKey(entry)
      const parsed = selectorFromScopeKey(key)
      expect(parsed, key).not.toBeNull()
      expect(scopeKey(parsed!), key).toBe(key)
    }
    expect(selectorFromScopeKey('account:482109375512')).toEqual({ kind: 'account', account_id: '482109375512', threshold: 1 })
    expect(selectorFromScopeKey('account:482109375512:eu-west-1')).toEqual({ kind: 'account', account_id: '482109375512', region: 'eu-west-1', threshold: 1 })
    expect(selectorFromScopeKey('cluster:abc')).toEqual({ kind: 'cluster', cluster_id: 'abc', threshold: 1 })
    expect(selectorFromScopeKey('service:xyz')).toEqual({ kind: 'service', service_id: 'xyz' })
    expect(selectorFromScopeKey('nonsense')).toBeNull()
  })

  it('labels each kind', () => {
    expect(scopeLabel(selector({ kind: 'account', account_id: '1', account_name: 'homelab-admin' }))).toBe('homelab-admin (all regions)')
    expect(scopeLabel(selector({ kind: 'account', account_id: '1', account_name: 'homelab-admin', region: 'eu-west-1' }))).toBe('homelab-admin / eu-west-1')
    expect(scopeLabel(CORE_SERVICES_CLUSTER)).toBe('core-services')
    expect(scopeLabel(IMAGE_RESIZER_SERVICE)).toBe('image-resizer · background-jobs')
    expect(scopeLabel(selector({ kind: 'aws', key: 'env', value: 'prod' }))).toBe('env=prod')
    expect(scopeLabel(selector({ kind: 'harbormaster', tag_id: 2, tag_name: 'Staging' }))).toBe('Staging')
  })

  it('knows which kinds are threshold entries', () => {
    expect(['account', 'cluster'].every((kind) => isGroupKind(kind as 'account'))).toBe(true)
    expect(['service', 'aws', 'harbormaster'].some((kind) => isGroupKind(kind as 'service'))).toBe(false)
  })
})

describe('scope options', () => {
  it('lists accounts with an all-regions entry and one entry per region', () => {
    const options = scopeOptions(NOTIFICATION_TARGETS, OFFHOURS_TAGS, [])
    const accounts = options.filter((option) => option.group === 'Accounts').map((option) => option.label)
    expect(accounts).toEqual([
      'homelab-admin (all regions)',
      'homelab-admin / us-east-1',
      'homelab-admin / eu-west-1',
      'homelab-admin / eu-central-1',
      'staging-example (all regions)',
      'staging-example / us-east-1',
    ])
    expect(options.filter((option) => option.group === 'Clusters').map((option) => option.label)).toEqual(['core-services', 'background-jobs', 'staging-web'])
    expect(options.filter((option) => option.group === 'Services').map((option) => option.label)).toContain('payments-api · core-services')
    expect(options.some((option) => option.group === 'Harbormaster tags')).toBe(true)
    expect(options.some((option) => option.group === 'AWS tags')).toBe(true)
  })

  it('keeps a saved selector that is no longer in the targets and marks it missing', () => {
    const gone = selector({ kind: 'service', service_id: 'gone-id', service: 'retired-api', cluster: 'core-services', missing: true })
    const options = scopeOptions(NOTIFICATION_TARGETS, OFFHOURS_TAGS, [gone, IMAGE_RESIZER_SERVICE])
    const kept = options.find((option) => option.key === 'service:gone-id')
    expect(kept).toMatchObject({ group: 'Services', missing: true, label: 'retired-api · core-services' })
    expect(options.find((option) => option.key === scopeKey(IMAGE_RESIZER_SERVICE))?.missing).toBeFalsy()
  })
})

describe('a deleted Harbormaster tag', () => {
  const deleted = selector({ kind: 'harbormaster', tag_id: 99, tag_name: '', missing: true })

  it('is labelled "Deleted tag" instead of an empty name', () => {
    expect(scopeLabel(deleted)).toBe('Deleted tag')
    expect(scopeLabel(selector({ kind: 'harbormaster', tag_id: 2, tag_name: 'Staging' }))).toBe('Staging')
  })

  it('is kept in the picker and marked missing, while a present or AWS tag is not', () => {
    const options = scopeOptions(NOTIFICATION_TARGETS, OFFHOURS_TAGS, [deleted])
    expect(options.find((option) => option.key === scopeKey(deleted))).toMatchObject({ group: 'Harbormaster tags', missing: true, label: 'Deleted tag' })
    const aws = selector({ kind: 'aws', key: 'team', value: 'gone' })
    expect(scopeOptions(NOTIFICATION_TARGETS, OFFHOURS_TAGS, [aws]).find((option) => option.key === scopeKey(aws))?.missing).toBeFalsy()
  })
})

describe('scope summary and thresholds', () => {
  it('summarises a scope as Everything or its first labels', () => {
    expect(scopeSummary({ ...ROUTE_CORE_SLACK, scope: [] })).toBe('Everything')
    expect(scopeSummary(ROUTE_RESIZER_TELEGRAM)).toBe('image-resizer')
    expect(scopeSummary(ROUTE_CORE_SLACK)).toBe('core-services (2+)')
    expect(scopeSummary(ROUTE_WEBHOOK_SIGNED)).toBe('env=staging, Staging +1')
  })

  it('phrases threshold 1 and threshold 3', () => {
    expect(thresholdPhrase(1)).toBe('when any service is failing')
    expect(thresholdPhrase(3)).toBe('when 3 or more services are failing')
  })

  it('starts a picked cluster at threshold 1 and clamps a typed threshold', () => {
    const draft = newRouteDraft([], 'UTC')
    const withCluster = addScopeEntry(draft, 'cluster:abc')
    expect(withCluster.scope).toEqual([{ key: 'cluster:abc', threshold: 1 }])
    expect(setThreshold(withCluster, 'cluster:abc', 3).scope[0].threshold).toBe(3)
    expect(setThreshold(withCluster, 'cluster:abc', 0).scope[0].threshold).toBe(1)
    expect(setThreshold(withCluster, 'cluster:abc', 9999).scope[0].threshold).toBe(500)
    expect(setThreshold(withCluster, 'cluster:abc', 2.6).scope[0].threshold).toBe(3)
    expect(setThreshold(withCluster, 'cluster:abc', Number.NaN).scope[0].threshold).toBe(1)
    // A service entry has no threshold to set.
    const withService = addScopeEntry(draft, 'service:xyz')
    expect(withService.scope).toEqual([{ key: 'service:xyz', threshold: 0 }])
    expect(setThreshold(withService, 'service:xyz', 5).scope[0].threshold).toBe(0)
  })

  it('refuses to add the same entry twice', () => {
    const once = addScopeEntry(newRouteDraft([], 'UTC'), 'cluster:abc')
    expect(addScopeEntry(once, 'cluster:abc')).toBe(once)
  })

  it('sends thresholds only for cluster and account entries', () => {
    let draft = newRouteDraft([], 'UTC')
    draft = { ...draft, url: 'https://hooks.slack.com/services/T/B/x' }
    for (const key of ['cluster:abc', 'account:111111111111', 'account:111111111111:eu-west-1', 'service:xyz', 'hm:4', 'aws:env=prod']) draft = addScopeEntry(draft, key)
    draft = setThreshold(setThreshold(draft, 'cluster:abc', 3), 'account:111111111111', 2)
    expect(bodyFromDraft(draft).scope).toEqual([
      { kind: 'cluster', cluster_id: 'abc', threshold: 3 },
      { kind: 'account', account_id: '111111111111', threshold: 2 },
      { kind: 'account', account_id: '111111111111', region: 'eu-west-1', threshold: 1 },
      { kind: 'service', service_id: 'xyz' },
      { kind: 'harbormaster', tag_id: 4 },
      { kind: 'aws', key: 'env', value: 'prod' },
    ])
  })
})

describe('route draft', () => {
  it('names a new route after the existing ones and starts it off', () => {
    expect(newRouteDraft([], 'Europe/Berlin')).toMatchObject({ name: 'New route 1', enabled: false, destination: 'slack', quietTimezone: 'Europe/Berlin', fallback: false })
    expect(newRouteDraft(['New route 1', 'new route 2'], 'UTC').name).toBe('New route 3')
    expect(newRouteDraft([], 'UTC').kinds).toHaveLength(7)
  })

  it('omits secrets that were not retyped and sends a retyped one', () => {
    const saved = ROUTE_CORE_SLACK
    const draft = draftFromRoute(saved)
    expect(draft.url).toBe('')
    const body = bodyFromDraft(draft, saved)
    expect(body).not.toHaveProperty('url')
    expect(body).not.toHaveProperty('routing_key')
    expect(body).not.toHaveProperty('bot_token')
    expect(body).not.toHaveProperty('signing_secret')
    expect(body.version).toBe(saved.version)
    expect(bodyFromDraft({ ...draft, url: ' https://hooks.slack.com/services/T/B/new ' }, saved).url).toBe('https://hooks.slack.com/services/T/B/new')
    const telegram = draftFromRoute(ROUTE_RESIZER_TELEGRAM)
    expect(bodyFromDraft({ ...telegram, botToken: TOKEN }, ROUTE_RESIZER_TELEGRAM).bot_token).toBe(TOKEN)
    expect(bodyFromDraft(telegram, ROUTE_RESIZER_TELEGRAM)).toMatchObject({ chat_id: '-1001234567890', thread_id: 7 })
  })

  it('sends an empty signing secret to clear it', () => {
    const draft = draftFromRoute(ROUTE_WEBHOOK_SIGNED)
    expect(bodyFromDraft(draft, ROUTE_WEBHOOK_SIGNED)).not.toHaveProperty('signing_secret')
    expect(bodyFromDraft({ ...draft, clearSigning: true }, ROUTE_WEBHOOK_SIGNED).signing_secret).toBe('')
    expect(bodyFromDraft({ ...draft, signingSecret: 's3cret' }, ROUTE_WEBHOOK_SIGNED).signing_secret).toBe('s3cret')
  })

  it('only sends the fields its destination uses', () => {
    const email = bodyFromDraft(draftFromRoute(ROUTE_STAGING_EMAIL), ROUTE_STAGING_EMAIL)
    expect(email).toMatchObject({ destination: 'email', recipients: ['platform@example.com'], chat_id: '', thread_id: null, pagerduty_region: '' })
    const telegram = bodyFromDraft(draftFromRoute(ROUTE_RESIZER_TELEGRAM), ROUTE_RESIZER_TELEGRAM)
    expect(telegram.recipients).toEqual([])
  })

  it('is dirty after any field changes and clean after reverting', () => {
    const saved = ROUTE_CORE_SLACK
    const clean = draftFromRoute(saved)
    expect(isDirty(clean, saved)).toBe(false)
    for (const changed of [
      { ...clean, name: 'other' },
      { ...clean, enabled: !clean.enabled },
      { ...clean, minSeverity: 'critical' as const },
      { ...clean, kinds: clean.kinds.slice(1) },
      { ...clean, repeatSeconds: 7200 as const },
      { ...clean, quietEnabled: !clean.quietEnabled },
      { ...clean, notifyResolved: !clean.notifyResolved },
      { ...clean, fallback: !clean.fallback },
      { ...clean, url: 'https://hooks.slack.com/services/T/B/new' },
      { ...clean, label: 'renamed' },
      setThreshold(clean, scopeKey(CORE_SERVICES_CLUSTER), 5),
      addScopeEntry(clean, 'service:xyz'),
    ]) {
      expect(isDirty(changed, saved)).toBe(true)
    }
    expect(isDirty(setThreshold(setThreshold(clean, scopeKey(CORE_SERVICES_CLUSTER), 5), scopeKey(CORE_SERVICES_CLUSTER), 2), saved)).toBe(false)
    expect(isDirty(newRouteDraft([], 'UTC'), undefined)).toBe(true)
  })
})

describe('validateDraft', () => {
  const fresh = (patch: Partial<ReturnType<typeof newRouteDraft>> = {}) => ({ ...newRouteDraft([], 'UTC'), ...patch })

  it('requires a URL for a new Slack route but not for a saved one', () => {
    expect(validateDraft(fresh(), undefined, CAPS)).toBe('Enter a Slack incoming webhook URL (https://hooks.slack.com/...).')
    expect(validateDraft(fresh({ url: 'https://hooks.slack.com/services/T/B/x' }), undefined, CAPS)).toBeNull()
    expect(validateDraft(draftFromRoute(ROUTE_CORE_SLACK), ROUTE_CORE_SLACK, CAPS)).toBeNull()
    // Switching the destination of a saved route needs the new destination's secret.
    expect(validateDraft({ ...draftFromRoute(ROUTE_CORE_SLACK), destination: 'webhook' }, ROUTE_CORE_SLACK, CAPS)).toBe('Enter an https URL.')
    // An unreadable stored secret must be entered again.
    expect(validateDraft(draftFromRoute({ ...ROUTE_CORE_SLACK, secret_unreadable: true }), { ...ROUTE_CORE_SLACK, secret_unreadable: true }, CAPS)).toBe('Enter the destination again.')
  })

  it('requires a bot token and chat id for a new Telegram route, and only the chat id for a saved one', () => {
    const base = fresh({ destination: 'telegram' })
    expect(validateDraft(base, undefined, CAPS)).toBe('Enter the bot token BotFather gave you.')
    expect(validateDraft({ ...base, botToken: TOKEN }, undefined, CAPS)).toBe('Enter a numeric chat ID or @channelname.')
    expect(validateDraft({ ...base, botToken: TOKEN, chatId: '-1001' }, undefined, CAPS)).toBeNull()
    expect(validateDraft(draftFromRoute(ROUTE_RESIZER_TELEGRAM), ROUTE_RESIZER_TELEGRAM, CAPS)).toBeNull()
    expect(validateDraft({ ...draftFromRoute(ROUTE_RESIZER_TELEGRAM), chatId: '' }, ROUTE_RESIZER_TELEGRAM, CAPS)).toBe('Enter a numeric chat ID or @channelname.')
    expect(validateDraft({ ...draftFromRoute(ROUTE_RESIZER_TELEGRAM), botToken: 'nope' }, ROUTE_RESIZER_TELEGRAM, CAPS)).toBe('Enter the bot token BotFather gave you.')
  })

  it('rejects a chat id that is neither a number nor @name', () => {
    const draft = fresh({ destination: 'telegram', botToken: TOKEN })
    for (const bad of ['ops', '@ab', '12ab', '1.5', '@1ops_alerts']) {
      expect(validateDraft({ ...draft, chatId: bad }, undefined, CAPS), bad).toBe('Enter a numeric chat ID or @channelname.')
    }
    for (const good of ['-1001234567890', '42', '@ops_alerts']) expect(validateDraft({ ...draft, chatId: good }, undefined, CAPS), good).toBeNull()
    expect(validateDraft({ ...draft, chatId: '-1', threadId: 'abc' }, undefined, CAPS)).toBe('Enter a topic ID, or leave it empty.')
    expect(validateDraft({ ...draft, chatId: '-1', threadId: '12' }, undefined, CAPS)).toBeNull()
  })

  it('rejects email when SMTP is not configured', () => {
    const email = fresh({ destination: 'email', recipients: 'ops@example.com' })
    expect(validateDraft(email, undefined, { ...CAPS, emailAvailable: false })).toBe('Set HM_SMTP_HOST to send email.')
    expect(validateDraft(email, undefined, CAPS)).toBeNull()
    expect(validateDraft({ ...email, recipients: ' ' }, undefined, CAPS)).toBe('Enter at least one email address.')
  })

  it('asks for HM_SESSION_KEY when secrets are unavailable', () => {
    expect(validateDraft(fresh({ url: 'https://hooks.slack.com/x' }), undefined, { ...CAPS, secretsAvailable: false })).toBe('Set HM_SESSION_KEY before adding notification routes.')
  })

  it('checks the name, the alert types and thresholds', () => {
    expect(validateDraft(fresh({ name: '  ' }), undefined, CAPS)).toBe('Enter a route name.')
    expect(validateDraft(fresh({ url: 'https://hooks.slack.com/x', kinds: [] }), undefined, CAPS)).toBe('Choose at least one alert type.')
  })
})

describe('preview sentences', () => {
  it('counts open groups and per-alert matches into one sentence', () => {
    expect(previewSentence(NOTIFICATION_PREVIEW)).toEqual({ text: 'Right now this route would send 2 notifications.', warn: false })
    expect(previewSentence({ ...NOTIFICATION_PREVIEW, notification_count: 1 })).toEqual({ text: 'Right now this route would send 1 notification.', warn: false })
    expect(previewSentence(NOTIFICATION_PREVIEW_GROUPS).text).toBe('Right now this route would send 1 notification.')
  })

  it('warns when nothing would be sent', () => {
    expect(previewSentence(NOTIFICATION_PREVIEW_EMPTY)).toEqual({
      text: 'This route matches no open alerts. Check the severity, scope and alert types.',
      warn: true,
    })
  })

  it('lists a group below its threshold as sending nothing', () => {
    expect(previewLines(NOTIFICATION_PREVIEW_GROUPS)).toEqual([
      'core-services: 2 of 5 services failing',
      'staging-example: 1 of 4 services failing, below 3. Nothing is sent.',
    ])
  })

  it('names unreachable accounts and per-alert matches', () => {
    const preview: NotificationPreview = {
      ...NOTIFICATION_PREVIEW,
      groups: [{ ...NOTIFICATION_PREVIEW_GROUPS.groups[0], label: 'homelab-admin', kind: 'account', unreachable: true }],
    }
    expect(previewLines(preview)).toEqual(['homelab-admin: unreachable', 'payments-api: service failing', 'image-resizer: restart loop'])
  })

  it('shortens a long list of matching services', () => {
    const matches = Array.from({ length: 8 }, (_, i) => ({
      service: `svc${i}`,
      cluster: 'c',
      kind: 'restart_loop',
      severity: 'warning' as const,
      title: `svc${i}: restart loop`,
    }))
    const lines = previewLines({ ...NOTIFICATION_PREVIEW, matches: [...matches, matches[0]], match_count: 9, groups: [] })
    expect(lines).toHaveLength(6)
    expect(lines[4]).toBe('svc4: restart loop')
    expect(lines[5]).toBe('and 3 more')
  })

  it('explains the settle time and hides the sentence when it is 0', () => {
    expect(settleSentence(0)).toBe('')
    expect(settleSentence(180)).toBe(
      'Scaling up or down is not a failure and is never notified. A service with no running tasks, or fewer than it should have, is reported once it has stayed that way for 3 minutes.',
    )
    expect(settleSentence(60)).toContain('for 1 minute.')
    expect(settleSentence(90)).toContain('for 2 minutes.')
    expect(pendingSentence(2, 180)).toBe('2 more may follow once they have lasted 3 minutes.')
  })
})

describe('delivery log', () => {
  it('labels each delivery status', () => {
    expect(statusPill(DELIVERY_DELIVERED)).toEqual({ kind: 'ok', label: 'Delivered' })
    expect(statusPill(DELIVERY_FAILED)).toEqual({ kind: 'bad', label: 'Failed' })
    expect(statusPill(DELIVERY_HELD)).toEqual({ kind: 'warn', label: 'Held' })
    expect(statusPill(DELIVERY_PENDING)).toEqual({ kind: 'mute', label: 'Pending' })
    expect(statusPill(DELIVERY_CANCELLED)).toEqual({ kind: 'mute', label: 'Not sent' })
  })

  it('formats the held note in local time', () => {
    const at = new Date(DELIVERY_HELD.held_until as string)
    const pad = (n: number) => String(n).padStart(2, '0')
    expect(deliveryNote(DELIVERY_HELD)).toBe(`Held by quiet hours until ${pad(at.getHours())}:${pad(at.getMinutes())}`)
    expect(deliveryNote(DELIVERY_FAILED)).toBe('HTTP 503 after 3 attempts')
    expect(deliveryNote(DELIVERY_CANCELLED)).toBe('Resolved during quiet hours; not sent.')
  })

  it('uses the singular for one failed delivery', () => {
    expect(failedBanner(1)).toBe('1 delivery failed in the last 24 hours.')
    expect(failedBanner(3)).toBe('3 deliveries failed in the last 24 hours.')
  })

  it('labels the update event', () => {
    expect(eventLabel('update')).toBe('Updated')
    expect(eventLabel('opened')).toBe('Opened')
    expect(eventLabel(DELIVERY_UPDATE.event)).toBe('Updated')
  })
})

describe('route helpers stay typed', () => {
  it('draft round trip keeps scope entries in stored order with thresholds', () => {
    const route: NotificationRoute = { ...ROUTE_CATCH_ALL, scope: [CORE_SERVICES_CLUSTER, IMAGE_RESIZER_SERVICE] }
    const draft = draftFromRoute(route)
    expect(draft.scope).toEqual([
      { key: scopeKey(CORE_SERVICES_CLUSTER), threshold: 2 },
      { key: scopeKey(IMAGE_RESIZER_SERVICE), threshold: 0 },
    ])
  })
})
