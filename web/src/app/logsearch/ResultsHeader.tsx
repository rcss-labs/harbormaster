import type { LogSearchResultDTO } from '../../api/logSearch'
import { Banner } from '../../components/Banner'
import { formatBytes, formatCost } from './model'
import styles from './logsearch.module.css'

const number = (value: number) => value.toLocaleString('en-US')

/** The count and scan summary, plus the notices that explain a result that is partial or missing a service. */
export function ResultsHeader({ result }: { result: LogSearchResultDTO }) {
  const seconds = (result.duration_ms / 1000).toFixed(1)
  const failedTargets = result.targets.filter((target) => target.state !== 'ok')
  const withoutAwslogs = result.services.filter((service) => service.state === 'no_awslogs')
  return (
    <>
      <div className={styles.resultsHead}>
        <strong className={styles.count}>{`${number(result.records_matched)} matching ${result.records_matched === 1 ? 'line' : 'lines'}`}</strong>
        <span className={styles.stats}>{`${formatBytes(result.bytes_scanned)} scanned in ${seconds} s · ${formatCost(result.bytes_scanned)}`}</span>
      </div>
      {(result.capped || failedTargets.length > 0 || withoutAwslogs.length > 0) && (
        <div className={styles.notices}>
          {result.capped && (
            <Banner kind="warn">
              {`Showing the newest ${number(result.rows.length)} of ${number(result.records_matched)} matching lines. The histogram and hits by service cover the lines shown.`}
            </Banner>
          )}
          {failedTargets.map((target) => (
            <Banner key={`${target.account_id}/${target.region}`} kind="warn">
              <span className={styles.noticeTarget}>{`${target.account_name || target.account_id} · ${target.region}`}</span>
              <span>{target.message || `The search in this account and region ended as ${target.state}.`}</span>
            </Banner>
          ))}
          {withoutAwslogs.map((service) => (
            <Banner key={service.id} kind="info">
              {`${service.name} has no container that logs to CloudWatch.`}
            </Banner>
          ))}
        </div>
      )}
    </>
  )
}
