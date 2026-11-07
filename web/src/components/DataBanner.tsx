import { useDataProblems } from '../api/observe'
import { Banner } from './Banner'

/** One compact notice when data this page depends on is stale. Renders nothing otherwise. */
export function DataBanner({ tiers }: { tiers: readonly string[] }) {
  const { affected } = useDataProblems(tiers)
  const labels = [...new Set(affected.flatMap((item) => item.failingTiers.map((tier) => tier.label)))]
  if (labels.length === 0) return null
  return (
    <Banner kind="warn" compact>
      Some data on this page is stale: {labels.join(', ')}
    </Banner>
  )
}
