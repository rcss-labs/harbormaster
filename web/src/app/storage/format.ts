import { fmtNum } from '../../charts/format'

const MIB = 1024 * 1024
const GIB = 1024 * MIB

/** Same rule as Go's storage.FormatSize: whole MiB below 1 GiB, otherwise GiB with one decimal. */
export function formatSize(bytes: number): string {
  if (bytes < GIB) return `${(bytes / MIB).toFixed(0)} MiB`
  return `${(bytes / GIB).toFixed(1)} GiB`
}

/** Throughput in MiB/s, using the chart number rule (whole numbers from 10 up, one decimal below). */
export function formatMiBps(value: number): string {
  return `${fmtNum(value)} MiB/s`
}
