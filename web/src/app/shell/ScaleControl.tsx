import { useState } from 'react'
import { Icon } from '../../components/Icon'
import { Popover } from '../../components/Popover'
import { Seg } from '../../components/Seg'
import { setScale, useScale, type Scale } from '../scale'
import styles from './shell.module.css'

const OPTIONS: { value: Scale; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: '100', label: '100 %' },
  { value: '125', label: '125 %' },
  { value: '150', label: '150 %' },
  { value: '200', label: '200 %' },
]

/** Top-bar button and popover that choose the UI scale (desktop only). */
export function ScaleControl() {
  const [open, setOpen] = useState(false)
  const { scale } = useScale()
  return (
    <div className={styles.popAnchor}>
      <button
        type="button"
        className={`${styles.iconBtn} ${open ? styles.btnOn : ''}`}
        aria-label="UI scale"
        aria-haspopup="true"
        aria-expanded={open}
        title="UI scale"
        onClick={() => setOpen((shown) => !shown)}
      >
        <Icon name="sliders" />
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label="UI scale" align="right" width={260} role="group">
        <div style={{ display: 'grid', gap: 8 }}>
          <Seg label="Scale" options={OPTIONS} value={scale} onChange={setScale} />
          <span style={{ color: 'var(--muted)', fontSize: 12 }}>Auto uses 150 % on windows 3000 px and wider.</span>
        </div>
      </Popover>
    </div>
  )
}
