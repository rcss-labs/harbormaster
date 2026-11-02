import { createContext, useContext, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

/** The element in the Settings header where the active tab puts its primary action. */
export const HeaderActionSlot = createContext<HTMLElement | null>(null)

/**
 * Renders `children` (the tab's primary action) into the Settings header. The button stays owned by the tab, so its
 * dialogs and state live with it; outside the Settings page it renders nothing.
 */
export function HeaderAction({ children }: { children: ReactNode }) {
  const slot = useContext(HeaderActionSlot)
  return slot ? createPortal(children, slot) : null
}
