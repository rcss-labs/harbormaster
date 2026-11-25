import type { ReviewRunDetailDTO } from '../../api/reviews'
import { Ago } from '../../components/Ago'
import { Pill } from '../../components/Pill'
import { ResultBody } from '../agentresult/ResultBody'
import { reviewPillKind, reviewStateText, reviewTypeLabel } from './reviewStateText'
import styles from './reviews.module.css'

const servicesInScope = (count: number) => `${count} ${count === 1 ? 'service' : 'services'} in scope`

/**
 * One review result: its name (admin text, a text node), type, period and scope, its state and times, then `ResultBody`.
 */
export function ReviewRunView({ run }: { run: ReviewRunDetailDTO }) {
  return (
    <div className={styles.view}>
      <section className={styles.block} aria-label="About">
        <p className={styles.name}>{run.name}</p>
        <p className={styles.meta}>
          <span>{reviewTypeLabel(run.type)}</span>
          <span className={styles.muted}>{run.period_label}</span>
        </p>
        <p className={styles.meta}>
          <span>{run.scope_label}</span>
          <span className={styles.muted}>{servicesInScope(run.services_total)}</span>
        </p>
        {run.test && <p className={styles.meta}>Test run. It was not sent.</p>}
        <p className={styles.state}>
          <Pill kind={reviewPillKind(run.state)}>{reviewStateText(run.state, run.reason)}</Pill>
          <span className={styles.muted}>
            Requested <Ago ts={run.requested_at} />
            {run.started_at !== null && (
              <>
                {' · started '}
                <Ago ts={run.started_at} />
              </>
            )}
            {run.finished_at !== null && (
              <>
                {' · finished '}
                <Ago ts={run.finished_at} />
              </>
            )}
          </span>
        </p>
      </section>
      <ResultBody summary={run.summary} summaryReplaced={run.summary_replaced} evidence={run.evidence} evidenceHidden={run.evidence_hidden} />
    </div>
  )
}
