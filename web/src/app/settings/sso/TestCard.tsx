import { useSearchParams } from 'react-router'
import { useSSOTestResult } from '../../../api/hooks'
import type { SSOConfigDTO, SSOTestResultDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import styles from './sso.module.css'

function outcomeText(result: SSOTestResultDTO): string | null {
  switch (result.outcome) {
    case 'would_create':
      return `Would create user ${result.username}`
    case 'would_sign_in':
      return `Would sign in ${result.username}`
    case 'would_link':
      return `Would link ${result.username} (pending link)`
    default:
      return null
  }
}

function Result({ result, onDismiss }: { result: SSOTestResultDTO; onDismiss: () => void }) {
  const outcome = outcomeText(result)
  const attributes = Object.entries(result.attributes)
  return (
    <div className={styles.stack}>
      {result.ok ? (
        <>
          <p className={styles.verdict}>Sign-in test passed.</p>
          {outcome && <p>{outcome}</p>}
          <label className={styles.field}>
            <span>NameID</span>
            <input
              className={`${styles.ctl} ${styles.mono}`}
              readOnly
              value={result.name_id}
              onFocus={(event) => event.target.select()}
            />
            <span className={styles.caption}>Use this value for pending links.</span>
          </label>
          <div className={styles.caption}>
            Format: <span className={styles.mono}>{result.name_id_format}</span>
          </div>
          {attributes.length > 0 && (
            <table className={styles.attrs}>
              <tbody>
                {attributes.map(([name, values]) => (
                  <tr key={name}>
                    <th scope="row">{name}</th>
                    <td className={styles.mono}>{values.join(', ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      ) : (
        <>
          <Banner kind="bad">{`Sign-in test failed: ${result.message}`}</Banner>
          <code className={styles.mono}>{result.reason}</code>
        </>
      )}
      <div className={styles.row}>
        <button type="button" className={styles.btn} onClick={onDismiss}>
          Dismiss
        </button>
      </div>
    </div>
  )
}

export function TestCard({ config }: { config: SSOConfigDTO }) {
  const [params, setParams] = useSearchParams()
  const testId = params.get('test')
  const result = useSSOTestResult(testId)

  const dismiss = () => {
    const next = new URLSearchParams(params)
    next.delete('test')
    setParams(next, { replace: true })
  }

  return (
    <Card title="Test sign-in">
      <div className={styles.stack}>
        <div className={styles.row}>
          {config.configured ? (
            <a className={styles.btn} href="/api/auth/saml/login?test=1" target="_blank" rel="noreferrer">
              Test sign-in
            </a>
          ) : (
            <button type="button" className={styles.btn} disabled>
              Test sign-in
            </button>
          )}
        </div>
        {testId !== null && result.isPending && <p className={styles.hint}>Loading the test result...</p>}
        {testId !== null && result.isError && (
          <>
            <p role="alert" className={styles.err}>
              {result.error.message}
            </p>
            <div className={styles.row}>
              <button type="button" className={styles.btn} onClick={dismiss}>
                Dismiss
              </button>
            </div>
          </>
        )}
        {testId !== null && result.data && <Result result={result.data} onDismiss={dismiss} />}
      </div>
    </Card>
  )
}
