import { describe, expect, it } from 'vitest'
import type { Definition } from '../task-definition/mainContainer'
import { ARCH_PARAMS, ARM64_REASON, arm64Href, parseArchParams, readArchitecture, setArchitecture } from './archParams'

const parse = (query: string) => parseArchParams(new URLSearchParams(query))

describe('archParams', () => {
  it('parses only arch=ARM64', () => {
    expect(parse('arch=ARM64')).toBe('ARM64')
    for (const query of ['', 'arch=', 'arch=arm64', 'arch=X86_64', 'arch=ARM64x', 'architecture=ARM64']) {
      expect(parse(query), query).toBeNull()
    }
  })

  it('builds the link a Cost row opens and names the parameter to clear', () => {
    expect(arm64Href('svc/1')).toBe('/services/svc%2F1/deploy?arch=ARM64')
    expect(ARCH_PARAMS).toEqual(['arch'])
    expect(ARM64_REASON).toBe('Move to ARM64 (Graviton)')
  })

  it('sets and reads the architecture, keeping the OS family', () => {
    const plain: Definition = { family: 'a', cpu: '256', containerDefinitions: [] }
    expect(readArchitecture(plain)).toBe('X86_64')

    const arm = setArchitecture(plain, 'ARM64')
    expect(readArchitecture(arm)).toBe('ARM64')
    expect(arm.runtimePlatform).toEqual({ cpuArchitecture: 'ARM64', operatingSystemFamily: 'LINUX' })
    expect(plain.runtimePlatform).toBeUndefined() // the input is never edited

    const windowsLike: Definition = { ...plain, runtimePlatform: { operatingSystemFamily: 'WINDOWS_SERVER_2019_CORE', cpuArchitecture: 'X86_64' } }
    expect(setArchitecture(windowsLike, 'ARM64').runtimePlatform).toEqual({ operatingSystemFamily: 'WINDOWS_SERVER_2019_CORE', cpuArchitecture: 'ARM64' })
  })

  it('returns to the deployed platform exactly when the architecture goes back', () => {
    const deployedWithout: Definition = { family: 'a', containerDefinitions: [] }
    const arm = setArchitecture(deployedWithout, 'ARM64', deployedWithout)
    expect(JSON.stringify(setArchitecture(arm, 'X86_64', deployedWithout))).toBe(JSON.stringify(deployedWithout))

    const deployedExplicit: Definition = { family: 'a', containerDefinitions: [], runtimePlatform: { cpuArchitecture: 'X86_64', operatingSystemFamily: 'LINUX' } }
    const armExplicit = setArchitecture(deployedExplicit, 'ARM64', deployedExplicit)
    expect(JSON.stringify(setArchitecture(armExplicit, 'X86_64', deployedExplicit))).toBe(JSON.stringify(deployedExplicit))
  })
})
