/** Permission actions as [key, label, letter]; each is one bit, in this order (1, 2, 4, 8, 16, 32, 64). Accept risk is in no preset, Full included. */
export const ACTIONS = [
  ['view', 'View', 'V'],
  ['logs', 'Logs', 'L'],
  ['operate', 'Operate', 'O'],
  ['deploy', 'Deploy', 'D'],
  ['edit', 'Edit task definition', 'E'],
  ['terminal', 'Terminal', 'T'],
  ['accept_risk', 'Accept risk', 'R'],
] as const

export const VIEW_BIT = 1

export const bitOf = (index: number): number => 1 << index

export const PRESETS: Record<string, number> = { None: 0, Viewer: 1, Operator: 7, Deployer: 31, Full: 63 }

/** Preset name matching the bitmask exactly, or "Custom". */
export function presetOf(actions: number): string {
  return Object.entries(PRESETS).find(([, value]) => value === actions)?.[0] ?? 'Custom'
}

/** Any action implies View; without View nothing is allowed. */
export function normalize(actions: number): number {
  return actions === 0 ? 0 : actions | VIEW_BIT
}

/** Flips one bit. Turning an action on also turns View on; turning View off clears everything. */
export function toggle(actions: number, bit: number): number {
  if ((actions & bit) === 0) return normalize(actions | bit)
  if (bit === VIEW_BIT) return 0
  return normalize(actions & ~bit)
}
