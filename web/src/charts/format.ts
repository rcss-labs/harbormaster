/** Compact number: 12345 → "12k", 12.34 → "12", 1.27 → "1.3" (mockup fmtNum). */
export const fmtNum = (value: number): string => {
  if (value >= 1000) return (value / 1000).toFixed(value >= 10000 ? 0 : 1) + 'k'
  if (value >= 10 || value === 0) return String(Math.round(value))
  return String(+value.toFixed(1))
}

/** Rounds up to 1, 2 or 5 significant steps of the value's order of magnitude (mockup niceMax). */
export const niceMax = (value: number): number => {
  const magnitude = Math.pow(10, Math.floor(Math.log10(Math.max(value, 1))))
  return Math.ceil(value / magnitude) * magnitude
}

/** Percentage of total, rounded; 0 when total is 0. */
export const pct = (value: number, total: number): number => (total ? Math.round((value / total) * 100) : 0)

/** Short date for a timeline tick, e.g. "Oct 31". */
export const dayLabel = (ts: number): string => new Date(ts).toLocaleDateString([], { month: 'short', day: 'numeric' })

export const XL24 = ['24 h ago', '18 h', '12 h', '6 h', 'now']
export const XL72 = ['72 h ago', '48 h', '24 h', 'now']
