import { useEffect, useRef, type RefObject } from 'react'

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * Moves focus into `containerRef` when the component mounts and puts it back on the element that had it
 * (the opener) when it unmounts. Returns the opener so callers can treat clicks on it as "not outside".
 */
export function useFocusReturn(containerRef: RefObject<HTMLElement | null>): RefObject<HTMLElement | null> {
  const openerRef = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const container = containerRef.current
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    if (container) (container.querySelector<HTMLElement>(FOCUSABLE) ?? container).focus()
    return () => {
      const opener = openerRef.current
      if (opener?.isConnected) opener.focus()
    }
  }, [containerRef])
  return openerRef
}

/** Keeps Tab and Shift+Tab inside `container`. */
export function trapTab(event: KeyboardEvent | React.KeyboardEvent, container: HTMLElement | null): void {
  if (event.key !== 'Tab' || !container) return
  const items = Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE))
  if (items.length === 0) {
    event.preventDefault()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement
  if (event.shiftKey && (active === first || !container.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !container.contains(active))) {
    event.preventDefault()
    first.focus()
  }
}
