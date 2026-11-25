import { Link } from 'react-router'
import type { InvestigationDetailDTO } from '../../api/investigations'
import { Ago } from '../../components/Ago'
import { Pill, type PillKind } from '../../components/Pill'
import { ResultBody } from '../agentresult/ResultBody'
import { stateText } from './stateText'
import styles from './investigations.module.css'

const pillKind = (state: string): PillKind => {
  if (state === 'done') return 'ok'
  if (state === 'failed') return 'bad'
  if (state === 'requested' || state === 'running') return 'info'
  return 'mute'
}

/**
 * One investigation: what it is about, the alert it followed, its state, then `ResultBody`.
 */
export function InvestigationView({ investigation }: { investigation: InvestigationDetailDTO }) {
  const { subject, members, evidence } = investigation
  const hasService = subject.service_id !== ''
  const state = stateText(investigation.state, investigation.reason)
  return (
    <div className={styles.view}>
      <section className={styles.block} aria-label="About">
        {hasService ? (
          <p>
            <Link to={`/services/${subject.service_id}/timeline`}>{subject.service}</Link>
            {subject.cluster !== '' && <span className={styles.muted}>{` · ${subject.cluster}`}</span>}
          </p>
        ) : (
          members.length > 0 && (
            <ul className={styles.members} aria-label="Services">
              {members.map((member, index) => (
                <li key={`${member.cluster}/${member.service}/${index}`}>
                  {member.service_id === '' ? member.service : <Link to={`/services/${member.service_id}/timeline`}>{member.service}</Link>}
                  {member.cluster !== '' && <span className={styles.muted}>{` · ${member.cluster}`}</span>}
                </li>
              ))}
            </ul>
          )
        )}
        <p className={styles.alert}>{investigation.alert.title}</p>
        <p className={styles.state}>
          <Pill kind={pillKind(investigation.state)}>{state}</Pill>
          <span className={styles.muted}>
            Requested <Ago ts={investigation.requested_at} />
            {investigation.finished_at !== null && (
              <>
                {' · finished '}
                <Ago ts={investigation.finished_at} />
              </>
            )}
          </span>
        </p>
      </section>
      <ResultBody summary={investigation.summary} summaryReplaced={investigation.summary_replaced} evidence={evidence} evidenceHidden={investigation.evidence_hidden} />
    </div>
  )
}
