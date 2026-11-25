import type { NotificationKind, NotificationPreview, NotificationRoute, NotificationTargets } from '../../../api/notifications'
import type { OffHoursTags } from '../../../api/offhours'
import { useAuth } from '../../auth/AuthProvider'
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { MessagePreview } from './MessagePreview'
import { ScopePicker } from './ScopePicker'
import {
  DESTINATIONS,
  REPEATS,
  addScopeEntry,
  removeScopeEntry,
  setThreshold,
  settleSentence,
  type RouteDraft,
} from './notifications'
import styles from './notifications.module.css'

interface RouteEditorProps {
  draft: RouteDraft
  onChange: (next: RouteDraft) => void
  saved: NotificationRoute | undefined
  isNew: boolean
  kinds: NotificationKind[]
  emailAvailable: boolean
  secretsAvailable: boolean
  settleSeconds: number
  targets: NotificationTargets | undefined
  tags: OffHoursTags | undefined
  preview: NotificationPreview | null
  previewError: string | null
  /** The first problem with the draft, or null. */
  problem: string | null
  dirty: boolean
  saving: boolean
  testing: boolean
  saveError: string | null
  onSave: () => void
  onDiscard: () => void
  onTest: () => void
  onDelete?: () => void
}

let zoneCache: string[] | null = null
function supportedZones(): string[] {
  if (zoneCache) return zoneCache
  const supported = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  zoneCache = supported.includes('UTC') ? supported : ['UTC', ...supported]
  return zoneCache
}

const SAVED_SECRET = 'Saved. Type to replace.'

export function RouteEditor(props: RouteEditorProps) {
  const { draft, onChange, saved, isNew, kinds, emailAvailable, secretsAvailable, settleSeconds, problem, dirty, saving, testing, saveError } = props
  const patch = (next: Partial<RouteDraft>) => onChange({ ...draft, ...next })
  // A stored secret belongs to the saved destination only.
  const secretPlaceholder = saved?.secret_set && saved.destination === draft.destination && !saved.secret_unreadable ? SAVED_SECRET : ''
  const canTest = !isNew && !dirty && saved !== undefined
  const zones = supportedZones()
  const zoneOptions = zones.includes(draft.quietTimezone) ? zones : [draft.quietTimezone, ...zones]
  const agentEnabled = useAuth().me?.agent_enabled ?? false
  const unseen = props.preview?.agent?.services_unseen ?? 0
  const needsSecretKey = draft.destination !== 'email' && !secretsAvailable

  return (
    <Panel title={isNew ? 'Add route' : 'Edit route'}>
      <div className={styles.editor}>
        {needsSecretKey && <Banner kind="warn">Set HM_SESSION_KEY before adding notification routes.</Banner>}
        {saved?.secret_unreadable && saved.destination === draft.destination && <Banner kind="warn">Enter the destination again.</Banner>}
        <div className={styles.fields}>
          <label className={styles.field}>
            <span>Route name</span>
            <input className={styles.ctl} value={draft.name} maxLength={80} onChange={(event) => patch({ name: event.target.value })} />
          </label>
          <label className={styles.field}>
            <span>Send to</span>
            <select className={styles.ctl} value={draft.destination} onChange={(event) => patch({ destination: event.target.value as RouteDraft['destination'] })}>
              {DESTINATIONS.map((destination) => (
                <option key={destination.value} value={destination.value} disabled={destination.value === 'email' && !emailAvailable}>
                  {destination.label}
                </option>
              ))}
            </select>
            {!emailAvailable && <span className={styles.hint}>Set HM_SMTP_HOST to send email.</span>}
          </label>

          {draft.destination === 'slack' && (
            <>
              <label className={styles.field}>
                <span>Channel (optional)</span>
                <input className={styles.ctl} value={draft.label} placeholder="#alerts" maxLength={80} onChange={(event) => patch({ label: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>Slack webhook URL</span>
                <input className={styles.ctl} type="password" autoComplete="off" value={draft.url} placeholder={secretPlaceholder} onChange={(event) => patch({ url: event.target.value })} />
              </label>
              <p className={`${styles.help} ${styles.wide}`}>
                One route posts to one Slack channel, the channel this incoming webhook belongs to. For another channel, add another route with that channel&apos;s webhook.
              </p>
            </>
          )}

          {draft.destination === 'telegram' && (
            <>
              <label className={styles.field}>
                <span>Bot token</span>
                <input className={styles.ctl} type="password" autoComplete="off" value={draft.botToken} placeholder={secretPlaceholder} onChange={(event) => patch({ botToken: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>Chat ID</span>
                <input className={styles.ctl} value={draft.chatId} placeholder="-1001234567890 or @channelname" onChange={(event) => patch({ chatId: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>Topic ID (optional)</span>
                <input className={styles.ctl} inputMode="numeric" value={draft.threadId} onChange={(event) => patch({ threadId: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>Label (optional)</span>
                <input className={styles.ctl} value={draft.label} maxLength={80} onChange={(event) => patch({ label: event.target.value })} />
              </label>
              <p className={`${styles.help} ${styles.wide}`}>Add the bot to the chat first. For a channel, make it an administrator.</p>
            </>
          )}

          {draft.destination === 'pagerduty' && (
            <>
              <label className={styles.field}>
                <span>Service (optional)</span>
                <input className={styles.ctl} value={draft.label} maxLength={80} onChange={(event) => patch({ label: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>PagerDuty integration key</span>
                <input className={styles.ctl} type="password" autoComplete="off" value={draft.routingKey} placeholder={secretPlaceholder} onChange={(event) => patch({ routingKey: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>PagerDuty region</span>
                <select className={styles.ctl} value={draft.pagerdutyRegion} onChange={(event) => patch({ pagerdutyRegion: event.target.value as 'us' | 'eu' })}>
                  <option value="us">US</option>
                  <option value="eu">EU</option>
                </select>
              </label>
            </>
          )}

          {draft.destination === 'webhook' && (
            <>
              <label className={styles.field}>
                <span>Webhook URL</span>
                <input className={styles.ctl} type="password" autoComplete="off" value={draft.url} placeholder={secretPlaceholder} onChange={(event) => patch({ url: event.target.value })} />
              </label>
              <label className={styles.field}>
                <span>Signing secret (optional)</span>
                <input
                  className={styles.ctl}
                  type="password"
                  autoComplete="off"
                  value={draft.signingSecret}
                  placeholder={saved?.signed && saved.destination === 'webhook' ? SAVED_SECRET : ''}
                  onChange={(event) => patch({ signingSecret: event.target.value, clearSigning: false })}
                />
              </label>
              {saved?.signed && saved.destination === 'webhook' && (
                <label className={`${styles.check} ${styles.wide}`}>
                  <input type="checkbox" checked={draft.clearSigning} onChange={(event) => patch({ clearSigning: event.target.checked, signingSecret: '' })} />
                  Remove the signing secret
                </label>
              )}
            </>
          )}

          {draft.destination === 'email' && (
            <label className={`${styles.field} ${styles.wide}`}>
              <span>Addresses</span>
              <input className={styles.ctl} value={draft.recipients} placeholder="ops@example.com, dev@example.com" onChange={(event) => patch({ recipients: event.target.value })} />
            </label>
          )}

          <div className={`${styles.field} ${styles.wide}`}>
            <span className={styles.fieldLabel}>Send alerts from</span>
            <ScopePicker
              entries={draft.scope}
              saved={saved?.scope ?? []}
              targets={props.targets}
              tags={props.tags}
              onAdd={(key) => onChange(addScopeEntry(draft, key))}
              onRemove={(key) => onChange(removeScopeEntry(draft, key))}
              onThreshold={(key, value) => onChange(setThreshold(draft, key, value))}
            />
            <label className={styles.check}>
              <input type="checkbox" checked={draft.fallback} onChange={(event) => patch({ fallback: event.target.checked })} />
              Only when no other route matches
            </label>
            <p className={styles.help}>Use this for a catch-all channel. It gets an alert only when no other enabled route matches it.</p>
          </div>

          <label className={styles.field}>
            <span>Send matching alerts</span>
            <select className={styles.ctl} value={draft.minSeverity} onChange={(event) => patch({ minSeverity: event.target.value as RouteDraft['minSeverity'] })}>
              <option value="warning">Warning and critical</option>
              <option value="critical">Critical only</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>Repeat while still open</span>
            <select className={styles.ctl} value={draft.repeatSeconds} onChange={(event) => patch({ repeatSeconds: Number(event.target.value) as RouteDraft['repeatSeconds'] })}>
              {REPEATS.map((repeat) => (
                <option key={repeat.value} value={repeat.value}>
                  {repeat.label}
                </option>
              ))}
            </select>
          </label>

          <fieldset className={`${styles.field} ${styles.wide}`} style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className={styles.fieldLabel}>Alert types</legend>
            <div className={styles.kinds}>
              {kinds.map((kind) => (
                <label key={kind.kind} className={styles.check}>
                  <input
                    type="checkbox"
                    checked={draft.kinds.includes(kind.kind)}
                    onChange={(event) => patch({ kinds: event.target.checked ? [...draft.kinds, kind.kind] : draft.kinds.filter((k) => k !== kind.kind) })}
                  />
                  {kind.label}
                </label>
              ))}
            </div>
            {settleSentence(settleSeconds) && <p className={styles.help}>{settleSentence(settleSeconds)}</p>}
          </fieldset>

          <div className={`${styles.field} ${styles.wide}`}>
            <label className={styles.check}>
              <input type="checkbox" checked={draft.quietEnabled} onChange={(event) => patch({ quietEnabled: event.target.checked })} />
              {`Hold warnings 22:00–08:00${draft.quietEnabled ? ` (${draft.quietTimezone})` : ''}`}
            </label>
            {draft.quietEnabled && (
              <label className={styles.field}>
                <span>Time zone</span>
                <select className={styles.ctl} value={draft.quietTimezone} onChange={(event) => patch({ quietTimezone: event.target.value })}>
                  {zoneOptions.map((zone) => (
                    <option key={zone} value={zone}>
                      {zone}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
          <label className={`${styles.check} ${styles.wide}`}>
            <input type="checkbox" checked={draft.notifyResolved} onChange={(event) => patch({ notifyResolved: event.target.checked })} />
            Notify when resolved
          </label>
          {agentEnabled && (
            <div className={`${styles.field} ${styles.wide}`}>
              <label className={styles.check}>
                <input type="checkbox" checked={draft.investigate} onChange={(event) => patch({ investigate: event.target.checked })} />
                Investigate with AI
              </label>
              {draft.investigate && unseen > 0 && props.preview?.agent && (
                <p className={styles.help}>{`The agent cannot see ${unseen} of this route's ${props.preview.agent.services_total} services. Change its access on Settings, AI agent.`}</p>
              )}
              {draft.investigate && draft.destination === 'pagerduty' && (
                <p className={styles.help}>PagerDuty gets no diagnosis message. The incident&apos;s link opens the investigation.</p>
              )}
              {draft.investigate && draft.destination === 'webhook' && <p className={styles.help}>Your receiver will get a new event value, diagnosis.</p>}
            </div>
          )}
          <label className={`${styles.check} ${styles.wide}`}>
            <input type="checkbox" checked={draft.enabled} onChange={(event) => patch({ enabled: event.target.checked })} />
            Route is on
          </label>
        </div>

        <MessagePreview preview={props.preview} error={props.previewError} settleSeconds={settleSeconds} />

        {saveError && <p className={styles.error}>{saveError}</p>}
        <div className={styles.actionsRow}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!dirty || problem !== null || saving} onClick={props.onSave}>
            Save route
          </button>
          <button type="button" className={styles.btn} disabled={!dirty || saving} onClick={props.onDiscard}>
            Discard changes
          </button>
          <button type="button" className={styles.btn} disabled={!canTest || testing} onClick={props.onTest}>
            Send test
          </button>
          {props.onDelete && (
            <button type="button" className={`${styles.btn} ${styles.danger} ${styles.spacer}`} onClick={props.onDelete}>
              Delete route
            </button>
          )}
        </div>
        {dirty && problem && <p className={styles.error}>{problem}</p>}
        {!canTest && <p className={styles.hint}>Save the route to send a test.</p>}
      </div>
    </Panel>
  )
}
