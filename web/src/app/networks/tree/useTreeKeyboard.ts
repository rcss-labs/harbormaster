import { useCallback, useLayoutEffect, useMemo, useRef, useState, type FocusEvent, type KeyboardEvent } from 'react'
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

  // The focused key and its ancestors (self first), captured when focus moves. If a refetch removes the focused node
  // the path still names its ancestors, so the tabbable item falls back to the nearest one that is visible.
  const [focusedPath, setFocusedPath] = useState<string[]>([root.key])
  const setFocusedKey = useCallback(
    (key: string) => {
      const path = [key]
      for (let parent = parentOf.get(key); parent !== undefined; parent = parentOf.get(parent)) path.push(parent)
      setFocusedPath(path)
    },
    [parentOf],
  )

  const tabbableKey = useMemo(() => {
    const visibleKeys = new Set(items.map((item) => item.node.key))
    return focusedPath.find((key) => visibleKeys.has(key)) ?? root.key
  }, [items, focusedPath, root.key])

  // The element that last held focus inside the tree, cleared when focus moves to something outside it. If a refetch or
  // a collapse removes that element, the browser drops focus to <body>; the effect below hands it to the tabbable item.
  const lastFocusedRef = useRef<HTMLElement | null>(null)
  const onFocusIn = useCallback((event: FocusEvent<HTMLElement>) => {
    lastFocusedRef.current = event.target as HTMLElement
  }, [])
  const onFocusOut = useCallback((event: FocusEvent<HTMLElement>) => {
    const next = event.relatedTarget as Node | null
    // Focus moved outside the tree, or to nothing while the element is still in the DOM (a real user blur). Only a
    // blur caused by the element's removal keeps the ref, so the layout effect can restore focus.
    if (next ? !event.currentTarget.contains(next) : (event.target as HTMLElement).isConnected) {
      lastFocusedRef.current = null
    }
  }, [])

  useLayoutEffect(() => {
    const lost = lastFocusedRef.current
    const active = document.activeElement
    if (!lost || lost.isConnected || (active && active !== document.body)) return
    const target = [...(containerRef.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])].find(
      (element) => element.dataset.key === tabbableKey,
    )
    target?.focus()
  }, [items, tabbableKey])

  const focusItem = useCallback((key: string) => {
    setFocusedKey(key)
    const elements = containerRef.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []
    for (const element of elements) {
      if (element.dataset.key === key) {
        element.focus()
        return
      }
    }
  }, [setFocusedKey])

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

  return { containerRef, tabbableKey, setFocusedKey, onItemKeyDown, onFocusIn, onFocusOut }
}
