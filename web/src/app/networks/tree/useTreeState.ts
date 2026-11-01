import { useCallback, useState } from 'react'
import type { TreeNode } from '../../../api/networks'

/** Keys of the nodes "Collapse to services" closes: services and the standalone-tasks node (their children are tasks). */
export function serviceKeys(root: TreeNode): string[] {
  const keys: string[] = []
  const visit = (node: TreeNode) => {
    if (node.kind === 'service' || node.kind === 'standalone') keys.push(node.key)
    node.children?.forEach(visit)
  }
  visit(root)
  return keys
}

interface TreeState {
  resetKey: string
  /** False until the first tree arrives for this resetKey; the default collapse is applied exactly once. */
  seeded: boolean
  closed: ReadonlySet<string>
}

/**
 * Collapse state of one VPC tree. Keys are stable across refetches, so the state survives data updates and is only
 * reset when `resetKey` (the VPC id) changes.
 */
export function useTreeState(root: TreeNode | undefined, collapsedByDefault: boolean, resetKey: string) {
  const [state, setState] = useState<TreeState>({ resetKey, seeded: false, closed: new Set() })

  // Adjusting state while rendering (instead of in an effect) avoids a flash of the fully expanded tree.
  let current = state
  if (current.resetKey !== resetKey) current = { resetKey, seeded: false, closed: new Set() }
  if (!current.seeded && root) {
    current = { resetKey, seeded: true, closed: new Set(collapsedByDefault ? serviceKeys(root) : []) }
  }
  if (current !== state) setState(current)

  const toggle = useCallback((key: string) => {
    setState((prev) => {
      const closed = new Set(prev.closed)
      if (!closed.delete(key)) closed.add(key)
      return { ...prev, seeded: true, closed }
    })
  }, [])

  const expandAll = useCallback(() => {
    setState((prev) => ({ ...prev, seeded: true, closed: new Set() }))
  }, [])

  const collapseToServices = useCallback(() => {
    if (!root) return
    setState((prev) => ({ ...prev, seeded: true, closed: new Set(serviceKeys(root)) }))
  }, [root])

  return { closed: current.closed, toggle, expandAll, collapseToServices }
}
