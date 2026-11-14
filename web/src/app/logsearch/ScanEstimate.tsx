import type { LogSearchEstimateDTO } from '../../api/logSearch'
import { Banner } from '../../components/Banner'
import { formatBytes, formatCost } from './model'
import styles from './logsearch.module.css'

/**
 * A rough guide to what the search may scan, shown before it runs: the stored size of every selected log group.
 * It is not a bound either way. It covers all retained logs, not just the time range (usually an overestimate),
 * but storedBytes is compressed while Insights bills the uncompressed bytes it scans (an underestimate). The scan
 * limit is the hard guard.
 */
export function ScanEstimate({ estimate }: { estimate: LogSearchEstimateDTO }) {
  const limit = formatBytes(estimate.scan_limit_bytes)
  const unknown = estimate.unknown_groups
  return (
    <>
      <p className={styles.hint} role="status" aria-label="Scan estimate">
        {`The selected log groups store about ${formatBytes(estimate.stored_bytes)} (${formatCost(estimate.stored_bytes)} if all of it were scanned). Treat it as a rough guide: the search reads only the chosen time range, but CloudWatch bills the uncompressed size, which can be larger than the stored size.`}
        {unknown > 0 && ` ${unknown === 1 ? '1 log group' : `${unknown} log groups`} could not be sized and ${unknown === 1 ? 'is' : 'are'} not counted.`}
      </p>
      {estimate.exceeds_scan_limit && (
        <Banner kind="warn">{`This may be more than the ${limit} scan limit. The search will stop at the ${limit} scan limit and show partial results.`}</Banner>
      )}
    </>
  )
}
