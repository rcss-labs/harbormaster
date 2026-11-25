import type { InvestigationEvidenceDTO } from '../../api/investigations'
import styles from './agentresult.module.css'

const HIDDEN_EVIDENCE = 'Some evidence is not shown because you do not have permission to read it.'
const CAPTION = 'Written by AI from what its identity may read. Check it before acting.'
const ACTION_LABEL: Record<string, string> = { view: 'View', logs: 'Logs' }

export interface ResultBodyProps {
  summary: string
  summaryReplaced: boolean
  evidence: InvestigationEvidenceDTO[]
  /** How many items the reader may not see; never named. */
  evidenceHidden: number
}

/**
 * The summary, its AI caption, the evidence and the hidden-evidence line, shared by investigations and reviews.
 * The summary, notes and excerpts come from a model reading untrusted data, so each is a plain text node:
 * no Markdown, no link, no image, no HTML.
 */
export function ResultBody({ summary, summaryReplaced, evidence, evidenceHidden }: ResultBodyProps) {
  return (
    <>
      {summary !== '' && (
        <section className={styles.block} aria-label="Summary">
          <p className={styles.summary}>{summary}</p>
          {!summaryReplaced && <p className={styles.muted}>{CAPTION}</p>}
        </section>
      )}
      {(evidence.length > 0 || evidenceHidden > 0) && (
        <section className={styles.block} aria-label="Evidence">
          <h3 className={styles.heading}>Evidence</h3>
          {evidence.map((item) => {
            // An action this app does not know is shown without its text: the permission it needed is unknown.
            const known = Object.hasOwn(ACTION_LABEL, item.action)
            return (
              <div key={item.ordinal} className={styles.item}>
                <p className={styles.itemHead}>
                  <span className={styles.tool}>{item.tool}</span>
                  {known && <span className={styles.muted}>{ACTION_LABEL[item.action]}</span>}
                  {item.service !== '' && <span className={styles.muted}>{item.service}</span>}
                </p>
                {known && item.note !== '' && <p>{item.note}</p>}
                {known && item.excerpt !== '' && <pre className={styles.excerpt}>{item.excerpt}</pre>}
              </div>
            )
          })}
          {evidenceHidden > 0 && <p className={styles.muted}>{HIDDEN_EVIDENCE}</p>}
        </section>
      )}
    </>
  )
}
