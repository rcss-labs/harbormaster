import { useScale } from '../scale'
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

export const SIDEBAR_STORAGE_KEY = 'harbormaster-sidebar'

/** Below this viewport width the sidebar collapses by itself. */
export const SIDEBAR_AUTO_COLLAPSE_BELOW = 1100
export const SIDEBAR_WIDE_PX = 212
export const SIDEBAR_NARROW_PX = 52

type SidebarChoice = 'collapsed' | 'expanded' | null

function readSidebarChoice(): SidebarChoice {
  try {
    const stored = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    return stored === 'collapsed' || stored === 'expanded' ? stored : null
  } catch {
    return null
  }
}

interface ShellStateValue {
  /** The window width in device-independent px, before the UI scale. */
  windowWidth: number
  /** The width the layout sees: the window width divided by the effective UI scale. */
  layoutWidth: number
  /** Same as `layoutWidth`. */
  width: number
  sidebarCollapsed: boolean
  toggleSidebar: () => void
  /** The activity drawer is open. It is never open by default and never docks beside the page. */
  railOpen: boolean
  toggleRail: () => void
  closeRail: () => void
  paletteOpen: boolean
  openPalette: () => void
  closePalette: () => void
  helpOpen: boolean
  openHelp: () => void
  closeHelp: () => void
}

const ShellStateContext = createContext<ShellStateValue | null>(null)

/** True for fields where typing a `[` must not toggle the sidebar. */
export function isEditable(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT'
}

/** True while a modal dialog is open, where a bare `[` must not change the page behind it. */
export const modalDialogOpen = () => document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]') !== null

export function ShellStateProvider({ children }: { children: ReactNode }) {
  const [windowWidth, setWindowWidth] = useState(() => window.innerWidth)
  const { effective } = useScale()
  const layoutWidth = windowWidth / (effective / 100)
  const [sidebarChoice, setSidebarChoice] = useState<SidebarChoice>(readSidebarChoice)
  const [railOpen, setRailOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)

  useEffect(() => {
    const onResize = () => setWindowWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const sidebarCollapsed = sidebarChoice ? sidebarChoice === 'collapsed' : layoutWidth < SIDEBAR_AUTO_COLLAPSE_BELOW

  const toggleSidebar = useCallback(() => {
    const next: SidebarChoice = sidebarCollapsed ? 'expanded' : 'collapsed'
    setSidebarChoice(next)
    try {
      localStorage.setItem(SIDEBAR_STORAGE_KEY, next)
    } catch {
      // storage unavailable: the choice lasts for this page view only
    }
  }, [sidebarCollapsed])

  const toggleRail = useCallback(() => setRailOpen((open) => !open), [])
  const closeRail = useCallback(() => setRailOpen(false), [])
  const openHelp = useCallback(() => setHelpOpen(true), [])
  const closeHelp = useCallback(() => setHelpOpen(false), [])
  const openPalette = useCallback(() => setPaletteOpen(true), [])
  const closePalette = useCallback(() => setPaletteOpen(false), [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((open) => !open)
        return
      }
      if (event.key === '[' && !event.metaKey && !event.ctrlKey && !event.altKey && !isEditable(event.target)) {
        if (paletteOpen || modalDialogOpen()) return
        toggleSidebar()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [paletteOpen, toggleSidebar])

  const value = useMemo<ShellStateValue>(
    () => ({
      windowWidth,
      layoutWidth,
      width: layoutWidth,
      sidebarCollapsed,
      toggleSidebar,
      railOpen,
      toggleRail,
      closeRail,
      paletteOpen,
      openPalette,
      closePalette,
      helpOpen,
      openHelp,
      closeHelp,
    }),
    [windowWidth, layoutWidth, sidebarCollapsed, toggleSidebar, railOpen, toggleRail, closeRail, paletteOpen, openPalette, closePalette, helpOpen, openHelp, closeHelp],
  )
  return <ShellStateContext.Provider value={value}>{children}</ShellStateContext.Provider>
}

export function useShellState(): ShellStateValue {
  const context = useContext(ShellStateContext)
  if (!context) throw new Error('useShellState must be used inside ShellStateProvider')
  return context
}
