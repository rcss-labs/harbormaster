import { Dialog } from '../../components/Dialog'
import { shortcutLabel } from './platform'
import styles from './shell.module.css'

/** Only shortcuts that exist are listed. */
const SHORTCUTS: { keys: string; description: string }[] = [
  { keys: shortcutLabel(), description: 'Search or run a command' },
  { keys: '[', description: 'Collapse or expand the sidebar' },
  { keys: 'g o', description: 'Go to Overview' },
  { keys: 'g s', description: 'Go to Services' },
  { keys: 'g c', description: 'Go to Clusters' },
  { keys: 'g t', description: 'Go to Tasks' },
  { keys: 'g n', description: 'Go to Networks' },
  { keys: 'g a', description: 'Go to Analytics' },
  { keys: '/', description: 'Filter the current page' },
  { keys: '?', description: 'Show this list' },
  { keys: 'esc', description: 'Close a drawer, popover or dialog' },
]

export function KeyboardHelp({ onClose }: { onClose: () => void }) {
  return (
    <Dialog title="Keyboard shortcuts" actions={[{ label: 'Close', onClick: onClose }]} onClose={onClose}>
      <dl className={styles.keys}>
        {SHORTCUTS.map((shortcut) => (
          <div key={shortcut.keys} className={styles.keyRow}>
            <dt>
              <kbd className={styles.kbd}>{shortcut.keys}</kbd>
            </dt>
            <dd>{shortcut.description}</dd>
          </div>
        ))}
      </dl>
    </Dialog>
  )
}
