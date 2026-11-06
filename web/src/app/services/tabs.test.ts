import { describe, expect, it } from 'vitest'
import { BOLD_TABS, TABS, TAB_LOCKS, isTabKey, isUnlocked } from './tabs'

const none = { view: true, logs: false, operate: false, deploy: false, edit: false, terminal: false }

describe('tabs', () => {
  it('isTabKey accepts deploy', () => {
    expect(isTabKey('deploy')).toBe(true)
    expect(isTabKey('nonsense')).toBe(false)
  })

  it('orders the tabs with Configure after Terminal, History last, and bolds only Deploy', () => {
    expect(TABS.map(([key]) => key)).toEqual([
      'overview', 'tasks', 'logs', 'metrics', 'terminal', 'deploy', 'task-definition', 'scaling', 'operations',
    ])
    expect(TABS.find(([key]) => key === 'operations')?.[1]).toBe('History')
    expect([...BOLD_TABS]).toEqual(['deploy'])
  })

  it('puts Deploy, Task definition and Scaling in the Configure group', () => {
    expect(TABS.filter(([, , group]) => group === 'configure').map(([key]) => key)).toEqual(['deploy', 'task-definition', 'scaling'])
  })

  it('an any-of lock opens for each listed permission and for nothing else', () => {
    const lock = TAB_LOCKS.deploy!
    expect(lock.action).toBe('Edit, Deploy or Operate')
    expect(isUnlocked(lock, none)).toBe(false)
    expect(isUnlocked(lock, { ...none, deploy: true })).toBe(true)
    expect(isUnlocked(lock, { ...none, edit: true })).toBe(true)
    expect(isUnlocked(lock, { ...none, operate: true })).toBe(true)
    expect(isUnlocked(lock, { ...none, logs: true, terminal: true })).toBe(false)
  })

  it('a single-permission lock still works', () => {
    expect(isUnlocked(TAB_LOCKS.logs!, { ...none, logs: true })).toBe(true)
    expect(isUnlocked(TAB_LOCKS.logs!, none)).toBe(false)
  })
})
