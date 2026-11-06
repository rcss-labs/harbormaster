/**
 * Layout assertions the pre-redesign UI fails legitimately. Each entry waives one check on the routes it matches, and
 * the owning lane's merge is what removes it (lane 2A/2C un-waive at wave 2). A waived check that no longer fails is
 * simply enforced and passes. Set HM_FLOCI_UNFIXME=1 to enforce every check anyway (what the lead does to verify).
 */
export type Check = 'overflow' | 'stacked-banners'

export interface Wave0Fixme {
  check: Check
  /** Matched against the route name used in routes.spec.ts. */
  route: RegExp
  /** Viewport widths the waiver applies to. */
  widths: number[]
  reason: string
}

const ALL_WIDTHS = [1440, 2560, 3840, 390]

export const WAVE0_FIXME: Wave0Fixme[] = [
  // The old PollerBanners render one banner per failing region and count the failing Auto scaling tier (floci has no
  // DescribeScheduledActions) as a stale region, so three banners stack on every page.
  {
    check: 'stacked-banners',
    route: /.*/,
    widths: ALL_WIDTHS,
    reason: 'old PollerBanners stack one banner per region (owner: lane 1B core-only status, lane 1A freshness control, lane 0D DataBanner)',
  },
  // Measured on the wave-0 branch against the seeded estate: these pages overflow main sideways at 390 px.
  { check: 'overflow', route: /^overview$/, widths: [390], reason: 'Overview overflows at 390 (owner: lane 1G)' },
  { check: 'overflow', route: /^cluster$/, widths: [390, 1440], reason: 'cluster detail overflows (owner: lane 1F)' },
  { check: 'overflow', route: /^service-(overview|tasks|logs|metrics|terminal|task-definition|scaling|operations)$/, widths: [390], reason: 'service page frame overflows at 390 (owner: lane 1H)' },
  { check: 'overflow', route: /^service-deploy$/, widths: [390], reason: 'Deploy tab overflows at 390 (owner: lane 1I)' },
  { check: 'overflow', route: /^settings-(tags|sso|audit)$/, widths: [390], reason: 'settings tab overflows at 390 (owner: lane 1L)' },
]

export function waivers(check: Check, route: string, width: number): string[] {
  if (process.env.HM_FLOCI_UNFIXME) return []
  return WAVE0_FIXME.filter((entry) => entry.check === check && entry.route.test(route) && entry.widths.includes(width)).map((entry) => entry.reason)
}
