import { BarChart } from '../../charts/BarChart'
import type { LogSearchResultDTO } from '../../api/logSearch'
import { histogramBuckets } from './model'
import styles from './logsearch.module.css'

const DAY_MS = 24 * 60 * 60 * 1000

/** UTC, like the line times; the date is added only when the range is longer than a day. */
function bucketLabel(ms: number, withDate: boolean): string {
  const iso = new Date(ms).toISOString()
  return withDate ? `${iso.slice(5, 10)} ${iso.slice(11, 16)}` : iso.slice(11, 16)
}

/** Lines per time bucket over the searched range. The strip scrolls inside its panel so a long range never widens the page. */
export function HitsHistogram({ result }: { result: LogSearchResultDTO }) {
  const from = Date.parse(result.from)
  const to = Date.parse(result.to)
  const buckets = histogramBuckets(result.rows, from, to)
  const withDate = to - from > DAY_MS
  const columns = buckets.map((bucket) => ({
    parts: [{ v: bucket.count, color: 'var(--accent)' }],
    title: `${bucketLabel(bucket.start, withDate)} UTC · ${bucket.count} ${bucket.count === 1 ? 'line' : 'lines'}`,
  }))
  const labelAt = [...new Set([0, Math.floor((buckets.length - 1) / 2), buckets.length - 1])]
  return (
    <div className={styles.scroller} data-scroll="x">
      <div className={styles.histInner} role="img" aria-label="Hits over time">
        <BarChart cols={columns} height={120} xLabels={labelAt.map((index) => [index, bucketLabel(buckets[index].start, withDate)])} />
      </div>
    </div>
  )
}
