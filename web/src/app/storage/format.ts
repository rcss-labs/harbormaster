import { fmtNum } from '../../charts/format'

const MIB = 1024 * 1024
const GIB = 1024 * MIB

/** Go's %.0f rounds ties to even, while JS toFixed rounds them up, so 2.5 MiB must become "2 MiB". */
function roundHalfToEven(value: number): number {
  const floor = Math.floor(value)
  const fraction = value - floor
  if (fraction !== 0.5) return fraction < 0.5 ? floor : floor + 1
  return floor % 2 === 0 ? floor : floor + 1
}

/** Same rule as Go's storage.FormatSize: whole MiB below 1 GiB, otherwise GiB with one decimal. */
export function formatSize(bytes: number): string {
  if (bytes < GIB) return `${roundHalfToEven(bytes / MIB)} MiB`
  return `${(bytes / GIB).toFixed(1)} GiB`
}

/** Throughput in MiB/s, using the chart number rule (whole numbers from 10 up, one decimal below). */
export function formatMiBps(value: number): string {
  return `${fmtNum(value)} MiB/s`
}
