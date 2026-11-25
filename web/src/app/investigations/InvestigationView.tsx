import { Link } from 'react-router'
import type { InvestigationDetailDTO } from '../../api/investigations'
import { Ago } from '../../components/Ago'
import { Pill, type PillKind } from '../../components/Pill'
import { stateText } from './stateText'
import styles from './investigations.module.css'

const HIDDEN_EVIDENCE = 'Some evidence is not shown because you do not have permission to read it.'
const ACTION_LABEL: Record<string, string> = { view: 'View', logs: 'Logs' }

const pillKind = (state: string): PillKind => {
  if (state === 'done') return 'ok'
  if (state === 'failed') return 'bad'
  if (state === 'requested' || state === 'running') return 'info'
  return 'mute'
}

/**
 * One investigation: what it is about, the alert it followed, its state, the summary, then the evidence.
 * The summary, notes and excerpts come from a model reading untrusted logs, so each is a plain text node:
 * no Markdown, no link, no image, no HTML.
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
              {members.map((member) => (
                <li key={member.service_id}>
                  <Link to={`/services/${member.service_id}/timeline`}>{member.service}</Link>
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
      {investigation.summary !== '' && (
        <section className={styles.block} aria-label="Summary">
          <p className={styles.summary}>{investigation.summary}</p>
        </section>
      )}
      {(evidence.length > 0 || investigation.evidence_hidden > 0) && (
        <section className={styles.block} aria-label="Evidence">
          <h3 className={styles.heading}>Evidence</h3>
          {evidence.map((item) => (
            <div key={item.ordinal} className={styles.item}>
              <p className={styles.itemHead}>
                <span className={styles.tool}>{item.tool}</span>
                <span className={styles.muted}>{ACTION_LABEL[item.action] ?? ''}</span>
                {item.service !== '' && <span className={styles.muted}>{item.service}</span>}
              </p>
              <p>{item.note}</p>
              <pre className={styles.excerpt}>{item.excerpt}</pre>
            </div>
          ))}
          {investigation.evidence_hidden > 0 && <p className={styles.muted}>{HIDDEN_EVIDENCE}</p>}
        </section>
      )}
    </div>
  )
}
