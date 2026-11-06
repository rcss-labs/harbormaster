import { useEffect, useRef } from 'react'
import { isEditable, modalDialogOpen } from './ShellState'

/** `g` then one of these keys goes to the page. */
export const GO_TARGETS: Record<string, string> = {
  o: '/',
  s: '/services',
  c: '/clusters',
  t: '/tasks',
  n: '/networks',
  a: '/analytics/delivery',
}

/** How long the `g` prefix waits for its second key. */
const GO_WINDOW_MS = 1000

export interface ShortcutOptions {
  /** True while the command palette is open; every shortcut is ignored then. */
  paletteOpen?: boolean
  openHelp?: () => void
}

/**
 * Page shortcuts: `g` then `o|s|c|t|n|a` navigates, `/` focuses the page's primary filter (`[data-hm-filter]`),
 * `?` opens the shortcut list. All are ignored while typing, in a modal dialog, with the palette open, or with a modifier held.
 */
export function useShortcuts(navigate: (to: string) => void, { paletteOpen = false, openHelp }: ShortcutOptions = {}): void {
  const latest = useRef({ navigate, paletteOpen, openHelp })
  useEffect(() => {
    latest.current = { navigate, paletteOpen, openHelp }
  })

  useEffect(() => {
    let goPending = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const clearPending = () => {
      goPending = false
      clearTimeout(timer)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.metaKey || event.ctrlKey || event.altKey) return
      if (latest.current.paletteOpen || isEditable(event.target) || modalDialogOpen()) {
        clearPending()
        return
      }
      if (goPending) {
        const target = GO_TARGETS[event.key]
        clearPending()
        if (target) {
          event.preventDefault()
          latest.current.navigate(target)
          return
        }
      }
      if (event.key === 'g') {
        goPending = true
        timer = setTimeout(clearPending, GO_WINDOW_MS)
      } else if (event.key === '/') {
        const filter = document.querySelector<HTMLElement>('[data-hm-filter]')
        if (filter) {
          event.preventDefault()
          filter.focus()
        }
      } else if (event.key === '?') {
        event.preventDefault()
        latest.current.openHelp?.()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      clearTimeout(timer)
    }
  }, [])
}
