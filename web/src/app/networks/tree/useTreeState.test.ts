import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { TreeNode } from '../../../api/networks'
import { buildTree, KEYS, SERVICE_KEYS } from './treeFixture'
import { useTreeState } from './useTreeState'

describe('useTreeState', () => {
  it('starts collapsed to services when collapsedByDefault', () => {
    const root = buildTree()
    const { result } = renderHook(() => useTreeState(root, true, 'vpc-0a1'))
    expect([...result.current.closed].sort()).toEqual([...SERVICE_KEYS].sort())
  })

  it('starts fully expanded when not collapsedByDefault', () => {
    const root = buildTree()
    const { result } = renderHook(() => useTreeState(root, false, 'vpc-0a1'))
    expect(result.current.closed.size).toBe(0)
  })

  it('expandAll clears', () => {
    const root = buildTree()
    const { result } = renderHook(() => useTreeState(root, true, 'vpc-0a1'))
    act(() => result.current.expandAll())
    expect(result.current.closed.size).toBe(0)
  })

  it('collapseToServices closes service and standalone keys only', () => {
    const root = buildTree()
    const { result } = renderHook(() => useTreeState(root, false, 'vpc-0a1'))
    act(() => result.current.toggle(KEYS.azA))
    act(() => result.current.collapseToServices())
    expect(result.current.closed.has(KEYS.azA)).toBe(false)
    expect(result.current.closed.has(KEYS.clusterA)).toBe(false)
    expect([...result.current.closed].sort()).toEqual([...SERVICE_KEYS].sort())
  })

  it('toggle closes then reopens a key', () => {
    const root = buildTree()
    const { result } = renderHook(() => useTreeState(root, false, 'vpc-0a1'))
    act(() => result.current.toggle(KEYS.payments))
    expect(result.current.closed.has(KEYS.payments)).toBe(true)
    act(() => result.current.toggle(KEYS.payments))
    expect(result.current.closed.has(KEYS.payments)).toBe(false)
  })

  it('keeps closed keys when the root object is replaced', () => {
    let root = buildTree()
    const { result, rerender } = renderHook(() => useTreeState(root, false, 'vpc-0a1'))
    act(() => result.current.toggle(KEYS.azB))
    root = buildTree()
    rerender()
    expect(result.current.closed.has(KEYS.azB)).toBe(true)
  })

  it('resets when resetKey changes', () => {
    const root = buildTree()
    let resetKey = 'vpc-0a1'
    const { result, rerender } = renderHook(() => useTreeState(root, true, resetKey))
    act(() => result.current.expandAll())
    resetKey = 'vpc-other'
    rerender()
    expect([...result.current.closed].sort()).toEqual([...SERVICE_KEYS].sort())
  })

  it('applies the default once the tree arrives after the first render', () => {
    const { result, rerender } = renderHook(
      ({ root }: { root: TreeNode | undefined }) => useTreeState(root, true, 'vpc-0a1'),
      { initialProps: { root: undefined as TreeNode | undefined } },
    )
    expect(result.current.closed.size).toBe(0)
    rerender({ root: buildTree() })
    expect(result.current.closed.size).toBe(SERVICE_KEYS.length)
  })
})
