import { beforeEach, describe, expect, it } from 'vitest'
import { ME_ADMIN, ME_OMER } from '../../test/fixtures'
import { applyScope, loadScopes, removeScope, saveScope } from './scopes'

beforeEach(() => localStorage.clear())

describe('scopes', () => {
  it('applying a saved scope drops tag ids outside visibleTagIds', () => {
    // omer can View Dev (1) and Staging (2) but has no View on Prod (3).
    const scope = { name: 'Everything', tag: [1, 3, 2] }
    expect(applyScope(scope, ME_OMER).tag).toEqual([1, 2])
    expect(applyScope({ name: 'Prod only', tag: [3] }, ME_OMER).tag).toBeUndefined()
    expect(applyScope(scope, ME_ADMIN).tag).toEqual([1, 2, 3])
  })

  it('applying a saved scope drops an account or region the user no longer has', () => {
    const applied = applyScope({ name: 'Gone', account: '999999999999', region: 'ap-south-1' }, ME_ADMIN)
    expect(applied.account).toBeUndefined()
    expect(applied.region).toBeUndefined()
    const kept = applyScope({ name: 'Kept', account: ME_ADMIN.accounts[0].account_id, region: ME_ADMIN.regions[0] }, ME_ADMIN)
    expect(kept.account).toBe(ME_ADMIN.accounts[0].account_id)
    expect(kept.region).toBe(ME_ADMIN.regions[0])
  })

  it('scopes are stored per user', () => {
    saveScope('pavel', { name: 'Prod', tag: [3] })
    expect(loadScopes('pavel')).toEqual([{ name: 'Prod', tag: [3] }])
    expect(loadScopes('omer')).toEqual([])
    expect(localStorage.getItem('harbormaster-scopes:pavel')).not.toBeNull()
  })

  it('saving under an existing name replaces it, and removing deletes it', () => {
    saveScope('pavel', { name: 'Prod', tag: [3] })
    saveScope('pavel', { name: 'Prod', region: 'eu-west-1' })
    expect(loadScopes('pavel')).toEqual([{ name: 'Prod', region: 'eu-west-1' }])
    expect(removeScope('pavel', 'Prod')).toEqual([])
    expect(loadScopes('pavel')).toEqual([])
  })

  it('ignores corrupt storage', () => {
    localStorage.setItem('harbormaster-scopes:pavel', '{nope')
    expect(loadScopes('pavel')).toEqual([])
    localStorage.setItem('harbormaster-scopes:pavel', JSON.stringify([{ name: 5 }, { name: 'ok', tag: ['x', 2] }]))
    expect(loadScopes('pavel')).toEqual([{ name: 'ok', tag: [2] }])
  })
})
