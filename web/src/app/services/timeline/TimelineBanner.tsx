import { useState } from 'react'
import type { TimelineIncident } from '../../../api/timeline'
import { Banner } from '../../../components/Banner'
import { formatEventTime } from './model'
import { triggerSentence, type DiffRow } from './revisionDiff'
import styles from './timeline.module.css'

const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

interface TimelineBannerProps {
  incident: TimelineIncident
  /** The trigger's diff rows: the server's plus the revision comparison, empty until they are known. */
  triggerDiff: DiffRow[]
  onShowChange: () => void
}

function plural(count: number, unit: string): string {
  return `${count} ${unit}${count === 1 ? '' : 's'}`
}

/** "3 minutes", rounded down to the largest whole unit. */
function durationText(ms: number): string {
  if (ms >= DAY_MS) return plural(Math.floor(ms / DAY_MS), 'day')
  if (ms >= HOUR_MS) return plural(Math.floor(ms / HOUR_MS), 'hour')
  if (ms >= MINUTE_MS) return plural(Math.floor(ms / MINUTE_MS), 'minute')
  return 'less than a minute'
}

/** What the incident most likely started with, what was done about it, or that nothing explains it. */
export function TimelineBanner({ incident, triggerDiff, onShowChange }: TimelineBannerProps) {
  // Read once: the date is only added to times older than a day, which a re-render does not change.
  const [now] = useState(() => Date.now())
  const firstFailure = Date.parse(incident.first_failure_at)

  let banner
  if (incident.trigger_unknown) {
    banner = (
      <Banner kind="warn">
        <strong>Tasks have been failing for a long time.</strong> Harbormaster holds too many recent failures to tell what started it. Check the Deploy tab for revision history.
      </Banner>
    )
  } else if (incident.rolled_back) {
    const { to_revision: revision, actor, at } = incident.rolled_back
    banner = (
      <Banner kind="info">
        Rollback to revision {revision} started by {actor}, {formatEventTime(at, now - Date.parse(at) > DAY_MS)}. The change appears on the timeline with its reason.
      </Banner>
    )
  } else if (incident.trigger) {
    const { trigger } = incident
    const change = trigger.to_revision !== null ? `revision ${trigger.to_revision}` : trigger.title.toLowerCase()
    const sentence = triggerSentence(triggerDiff)
    banner = (
      <Banner
        kind="warn"
        action={
          <button type="button" className={styles.action} onClick={onShowChange}>
            Show the change
          </button>
        }
      >
        <strong>{`Tasks began failing ${durationText(Math.max(0, firstFailure - Date.parse(trigger.at)))} after ${change}.`}</strong>
        {sentence && ` ${sentence}`}
        {incident.top_reason && ` ${incident.top_reason_count} of ${incident.failure_count} failures since then: ${incident.top_reason}.`}
      </Banner>
    )
  } else {
    banner = (
      <Banner kind="warn">
        <strong>Tasks began failing at {formatEventTime(incident.first_failure_at, now - firstFailure > DAY_MS)}.</strong> No change was recorded in the 24 hours before.
      </Banner>
    )
  }

  return (
    <div className={styles.bannerBlock}>
      {banner}
      <p className={styles.hint}>Harbormaster sees its own changes and new deployments, not other changes made directly in AWS.</p>
    </div>
  )
}
