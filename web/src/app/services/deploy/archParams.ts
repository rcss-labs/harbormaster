import type { Definition } from '../task-definition/mainContainer'

export const ARCH_PARAMS = ['arch'] as const
export const ARM64_REASON = 'Move to ARM64 (Graviton)'

export type Architecture = 'X86_64' | 'ARM64'

/** The Deploy tab link of an ARM64 suggestion. The value is fixed: nothing from the URL is ever used as free text. */
export function arm64Href(serviceId: string): string {
  return `/services/${encodeURIComponent(serviceId)}/deploy?arch=ARM64`
}

/** The suggestion in the URL, or null unless it is exactly `arch=ARM64`. */
export function parseArchParams(params: URLSearchParams): 'ARM64' | null {
  return params.get('arch') === 'ARM64' ? 'ARM64' : null
}

type RuntimePlatform = Record<string, unknown>

const platformOf = (def: Definition | undefined): RuntimePlatform | undefined => {
  const value = def?.runtimePlatform
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as RuntimePlatform) : undefined
}

/** A definition without a runtime platform, or without a CPU architecture, is x86. */
export function readArchitecture(def: Definition): Architecture {
  return platformOf(def)?.cpuArchitecture === 'ARM64' ? 'ARM64' : 'X86_64'
}

/**
 * The definition with its CPU architecture set, keeping the operating system family (LINUX when there is none). Going back to
 * the architecture of `deployed` restores its runtime platform exactly, so choosing ARM64 and then x86 again leaves nothing
 * to review. The input is never edited.
 */
export function setArchitecture(def: Definition, arch: Architecture, deployed?: Definition): Definition {
  if (deployed !== undefined && arch === readArchitecture(deployed)) {
    const { runtimePlatform: _drop, ...rest } = def
    void _drop
    const original = platformOf(deployed)
    return original === undefined ? rest : { ...rest, runtimePlatform: { ...original } }
  }
  const current = platformOf(def) ?? {}
  return { ...def, runtimePlatform: { ...current, cpuArchitecture: arch, operatingSystemFamily: current.operatingSystemFamily ?? 'LINUX' } }
}
