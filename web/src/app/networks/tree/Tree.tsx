import type { TreeNode } from '../../../api/networks'
import { TreeItem } from './TreeNode'
import styles from './tree.module.css'
import { useTreeKeyboard } from './useTreeKeyboard'

interface TreeProps {
  root: TreeNode
  closed: ReadonlySet<string>
  onToggle(key: string): void
  ariaLabel: string
}

/** Left-to-right network tree: ARIA tree with roving tabindex; connectors are CSS pseudo-elements. */
export function Tree({ root, closed, onToggle, ariaLabel }: TreeProps) {
  const { containerRef, tabbableKey, setFocusedKey, onItemKeyDown, onFocusIn, onFocusOut } = useTreeKeyboard(root, closed, onToggle)
  return (
    <div className={styles.tree} ref={containerRef} onFocus={onFocusIn} onBlur={onFocusOut}>
      <ul role="tree" aria-label={ariaLabel}>
        <TreeItem
          node={root}
          level={1}
          closed={closed}
          tabbableKey={tabbableKey}
          onToggle={onToggle}
          onItemFocus={setFocusedKey}
          onItemKeyDown={onItemKeyDown}
        />
      </ul>
    </div>
  )
}
