import type { LogSearchEstimateDTO } from '../../api/logSearch'
import { Banner } from '../../components/Banner'
import { formatBytes, formatCost } from './model'
import styles from './logsearch.module.css'

/**
 * What the search may scan, shown before it runs. It is an upper bound: the stored size of every selected log
 * group, which covers all the retained logs and not just the chosen time range.
 */
export function ScanEstimate({ estimate }: { estimate: LogSearchEstimateDTO }) {
  const limit = formatBytes(estimate.scan_limit_bytes)
  const unknown = estimate.unknown_groups
  return (
    <>
      <p className={styles.hint} role="status" aria-label="Scan estimate">
        {`This search will scan at most ${formatBytes(estimate.stored_bytes)} (${formatCost(estimate.stored_bytes)}). That is the stored size of the selected log groups, not just the time range, so the real scan is usually smaller.`}
        {unknown > 0 && ` ${unknown === 1 ? '1 log group' : `${unknown} log groups`} could not be sized and ${unknown === 1 ? 'is' : 'are'} not counted.`}
      </p>
      {estimate.exceeds_scan_limit && (
        <Banner kind="warn">{`This may be more than the ${limit} scan limit. The search will stop at the ${limit} scan limit and show partial results.`}</Banner>
      )}
    </>
  )
}
