import { describe, expect, it } from 'vitest'
import type { Definition } from '../task-definition/mainContainer'
import {
  containerNames,
  setCommand,
  setContainerImageTag,
  setEntryPoint,
  setEnvironment,
  setHealthCheck,
  setSecrets,
  setTaskSize,
} from './draft'

const base = (): Definition => ({
  family: 'api',
  cpu: '512',
  memory: '1024',
  containerDefinitions: [
    { name: 'api', image: 'repo/api:1.0.0', essential: true, command: ['serve'], environment: [{ name: 'A', value: '1' }] },
    { name: 'migrator', image: 'repo/migrator:2.0.0', essential: false },
  ],
})

const containerOf = (def: Definition, name: string) => (def.containerDefinitions as Record<string, unknown>[]).find((entry) => entry.name === name)!

describe('draft reducers: each change touches only its path', () => {
  it('lists container names in order', () => {
    expect(containerNames(base())).toEqual(['api', 'migrator'])
  })

  it.each([
    ['setContainerImageTag', (def: Definition) => setContainerImageTag(def, 'migrator', '2.1.0'), 'image', 'repo/migrator:2.1.0'],
    ['setEnvironment', (def: Definition) => setEnvironment(def, 'migrator', [{ name: 'X', value: 'y' }]), 'environment', [{ name: 'X', value: 'y' }]],
    ['setSecrets', (def: Definition) => setSecrets(def, 'migrator', [{ name: 'S', valueFrom: '/prod/s' }]), 'secrets', [{ name: 'S', valueFrom: '/prod/s' }]],
    ['setCommand', (def: Definition) => setCommand(def, 'migrator', ['run', '--once']), 'command', ['run', '--once']],
    ['setEntryPoint', (def: Definition) => setEntryPoint(def, 'migrator', ['/bin/sh', '-c']), 'entryPoint', ['/bin/sh', '-c']],
    [
      'setHealthCheck',
      (def: Definition) => setHealthCheck(def, 'migrator', { command: ['CMD-SHELL', 'true'], interval: 30, timeout: 5, retries: 3, startPeriod: 0 }),
      'healthCheck',
      { command: ['CMD-SHELL', 'true'], interval: 30, timeout: 5, retries: 3, startPeriod: 0 },
    ],
  ])('%s writes one key of one container and leaves the input untouched', (_name, change, key, expected) => {
    const before = base()
    const snapshot = structuredClone(before)
    const after = change(before)

    expect(before).toEqual(snapshot)
    expect(containerOf(after, 'migrator')[key]).toEqual(expected)
    expect(containerOf(after, 'api')).toEqual(containerOf(snapshot, 'api'))
    expect({ ...after, containerDefinitions: undefined }).toEqual({ ...snapshot, containerDefinitions: undefined })
    const withoutKey = (container: Record<string, unknown>) => Object.fromEntries(Object.entries(container).filter(([name]) => name !== key))
    expect(withoutKey(containerOf(after, 'migrator'))).toEqual(withoutKey(containerOf(snapshot, 'migrator')))
  })

  it('removes the key when a list is emptied or the health check is cleared', () => {
    const withAll = setHealthCheck(setCommand(base(), 'api', ['serve']), 'api', { command: ['CMD'], interval: 30, timeout: 5, retries: 3, startPeriod: 0 })
    expect('command' in containerOf(setCommand(withAll, 'api', []), 'api')).toBe(false)
    expect('entryPoint' in containerOf(setEntryPoint(base(), 'api', []), 'api')).toBe(false)
    expect('environment' in containerOf(setEnvironment(base(), 'api', []), 'api')).toBe(false)
    expect('secrets' in containerOf(setSecrets(base(), 'api', []), 'api')).toBe(false)
    expect('healthCheck' in containerOf(setHealthCheck(withAll, 'api', null), 'api')).toBe(false)
  })

  it('setTaskSize changes only cpu and memory', () => {
    const before = base()
    const after = setTaskSize(before, { cpu: '1024', memory: '2048' })
    expect(after.cpu).toBe('1024')
    expect(after.memory).toBe('2048')
    expect(after.containerDefinitions).toBe(before.containerDefinitions)
    expect(setTaskSize(before, { memory: '2048' }).cpu).toBe('512')
  })

  it('an unknown container name changes nothing', () => {
    const before = base()
    expect(setCommand(before, 'nope', ['x'])).toEqual(before)
  })
})
