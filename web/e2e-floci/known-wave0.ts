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

/**
 * Every wave-0 waiver passed on the merged wave-1 branch (lane 1Z ran the routes with HM_FLOCI_UNFIXME=1), so none
 * remain. A waiver may only be added back with a reason that names an owner and the floci limitation behind it.
 */
export const WAVE0_FIXME: Wave0Fixme[] = []

export function waivers(check: Check, route: string, width: number): string[] {
  if (process.env.HM_FLOCI_UNFIXME) return []
  return WAVE0_FIXME.filter((entry) => entry.check === check && entry.route.test(route) && entry.widths.includes(width)).map((entry) => entry.reason)
}
