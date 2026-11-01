import type { KeyboardEvent } from 'react'
import type { TreeNode as TreeNodeData } from '../../../api/networks'
import { NodeBody } from './NodeBody'
import styles from './tree.module.css'

interface TreeItemProps {
  node: TreeNodeData
  level: number
  closed: ReadonlySet<string>
  tabbableKey: string
  onToggle(key: string): void
  onItemFocus(key: string): void
  onItemKeyDown(event: KeyboardEvent<HTMLElement>, node: TreeNodeData): void
}

/** One treeitem: its node box plus, when open, a `role="group"` list of its children. */
export function TreeItem(props: TreeItemProps) {
  const { node, level, closed, tabbableKey, onToggle, onItemFocus, onItemKeyDown } = props
  const children = node.children ?? []
  const hasChildren = children.length > 0
  const isClosed = closed.has(node.key)
  return (
    <li
      role="treeitem"
      aria-level={level}
      aria-expanded={hasChildren ? !isClosed : undefined}
      aria-selected={tabbableKey === node.key}
      aria-label={node.label}
      tabIndex={tabbableKey === node.key ? 0 : -1}
      data-key={node.key}
      className={styles.item}
      onFocus={(event) => {
        if (event.target === event.currentTarget) onItemFocus(node.key)
      }}
      onKeyDown={(event) => onItemKeyDown(event, node)}
    >
      <NodeBody node={node} closed={isClosed} onToggle={onToggle} />
      {hasChildren && !isClosed && (
        <ul role="group">
          {children.map((child) => (
            <TreeItem key={child.key} {...props} node={child} level={level + 1} />
          ))}
        </ul>
      )}
      {node.kind === 'subnet' && node.tasks === 0 && (
        <ul role="group">
          <li role="none">
            <div className={`${styles.node} ${styles.none}`}>No running tasks</div>
          </li>
        </ul>
      )}
    </li>
  )
}
