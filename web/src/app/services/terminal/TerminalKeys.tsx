import { createPortal } from 'react-dom'

const KEYS: { label: string; aria: string; bytes: string }[] = [
  { label: 'esc', aria: 'Send Escape', bytes: '\x1b' },
  { label: 'tab', aria: 'Send Tab', bytes: '\t' },
  { label: '^C', aria: 'Send Control-C', bytes: '\x03' },
  { label: '↑', aria: 'Send Up', bytes: '\x1b[A' },
  { label: '↓', aria: 'Send Down', bytes: '\x1b[B' },
  { label: '←', aria: 'Send Left', bytes: '\x1b[D' },
  { label: '→', aria: 'Send Right', bytes: '\x1b[C' },
]

/**
 * Keys a phone keyboard lacks, pinned above the on-screen keyboard while a session is connected.
 * Pressing a key never takes focus from the terminal, so the keyboard stays up.
 */
export function TerminalKeys({ send }: { send: (bytes: string) => void }) {
  // Portalled to the body: a fixed bar does not belong inside the scrolling page content.
  return createPortal(
    <div className="hm-keys" role="toolbar" aria-label="Terminal keys">
      {KEYS.map((key) => (
        <button key={key.aria} type="button" aria-label={key.aria} onMouseDown={(event) => event.preventDefault()} onClick={() => send(key.bytes)}>
          {key.label}
        </button>
      ))}
    </div>,
    document.body,
  )
}
