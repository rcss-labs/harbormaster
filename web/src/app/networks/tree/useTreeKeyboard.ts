import { useCallback, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router'
import { nodeHref, type TreeNode } from '../../../api/networks'

interface VisibleItem {
  node: TreeNode
  parentKey: string | undefined
}

/** Items in document order, skipping the children of closed nodes. */
function visibleItems(root: TreeNode, closed: ReadonlySet<string>): VisibleItem[] {
  const items: VisibleItem[] = []
  const visit = (node: TreeNode, parentKey: string | undefined) => {
    items.push({ node, parentKey })
    if (!closed.has(node.key)) node.children?.forEach((child) => visit(child, node.key))
  }
  visit(root, undefined)
  return items
}

/**
 * Roving tabindex and WAI-ARIA tree keyboard model. `focusedKey` is React state; the tabbable item is derived from it
 * and falls back to the nearest visible ancestor when the focused item disappears (collapsed or removed by a refetch).
 */
export function useTreeKeyboard(root: TreeNode, closed: ReadonlySet<string>, onToggle: (key: string) => void) {
  const navigate = useNavigate()
  const containerRef = useRef<HTMLDivElement>(null)
  const [focusedKey, setFocusedKey] = useState(root.key)

  const items = useMemo(() => visibleItems(root, closed), [root, closed])

  const parentOf = useMemo(() => {
    const parents = new Map<string, string>()
    const visit = (node: TreeNode) =>
      node.children?.forEach((child) => {
        parents.set(child.key, node.key)
        visit(child)
      })
    visit(root)
    return parents
  }, [root])

  const tabbableKey = useMemo(() => {
    const visibleKeys = new Set(items.map((item) => item.node.key))
    let key: string | undefined = focusedKey
    while (key !== undefined && !visibleKeys.has(key)) key = parentOf.get(key)
    return key ?? root.key
  }, [items, focusedKey, parentOf, root.key])

  const focusItem = useCallback((key: string) => {
    setFocusedKey(key)
    const elements = containerRef.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []
    for (const element of elements) {
      if (element.dataset.key === key) {
        element.focus()
        return
      }
    }
  }, [])

  const onItemKeyDown = useCallback(
    (event: KeyboardEvent<HTMLElement>, node: TreeNode) => {
      // Only the focused treeitem handles keys; ancestors also receive the bubbled event.
      if (event.target !== event.currentTarget || event.altKey || event.ctrlKey || event.metaKey) return
      const index = items.findIndex((item) => item.node.key === node.key)
      if (index < 0) return
      const hasChildren = (node.children?.length ?? 0) > 0
      const isClosed = closed.has(node.key)
      let handled = true
      switch (event.key) {
        case 'ArrowDown':
          if (index + 1 < items.length) focusItem(items[index + 1].node.key)
          break
        case 'ArrowUp':
          if (index > 0) focusItem(items[index - 1].node.key)
          break
        case 'ArrowRight':
          if (hasChildren && isClosed) onToggle(node.key)
          else if (hasChildren) focusItem(node.children![0].key)
          break
        case 'ArrowLeft':
          if (hasChildren && !isClosed) onToggle(node.key)
          else if (items[index].parentKey !== undefined) focusItem(items[index].parentKey!)
          break
        case 'Home':
          focusItem(items[0].node.key)
          break
        case 'End':
          focusItem(items[items.length - 1].node.key)
          break
        case 'Enter':
        case ' ': {
          const href = nodeHref(node)
          if (href) navigate(href)
          else if (hasChildren) onToggle(node.key)
          break
        }
        default:
          handled = false
      }
      if (handled) event.preventDefault()
    },
    [items, closed, focusItem, onToggle, navigate],
  )

  return { containerRef, tabbableKey, setFocusedKey, onItemKeyDown }
}
