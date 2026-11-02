import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

export const SIDEBAR_STORAGE_KEY = 'harbormaster-sidebar'

/** Below this viewport width the sidebar collapses by itself. */
export const SIDEBAR_AUTO_COLLAPSE_BELOW = 1100
/** From this viewport width the activity rail is docked beside the page; below it opens as an overlay. */
export const RAIL_DOCK_FROM = 1280
export const SIDEBAR_WIDE_PX = 212
export const SIDEBAR_NARROW_PX = 52
export const RAIL_PX = 300
/** The live pulse in the top bar needs this much room. */
const PULSE_MIN_MAIN_PX = 900

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
  width: number
  sidebarCollapsed: boolean
  toggleSidebar: () => void
  /** The rail is shown (docked or as overlay). */
  railOpen: boolean
  railDocked: boolean
  toggleRail: () => void
  closeRail: () => void
  paletteOpen: boolean
  openPalette: () => void
  closePalette: () => void
  /** Width left for the page itself. */
  mainWidth: number
  showPulse: boolean
}

const ShellStateContext = createContext<ShellStateValue | null>(null)

/** True for fields where typing a `[` must not toggle the sidebar. */
function isEditable(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT'
}

/** True while a modal dialog is open, where a bare `[` must not change the page behind it. */
const modalDialogOpen = () => document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]') !== null

export function ShellStateProvider({ children }: { children: ReactNode }) {
  const [width, setWidth] = useState(() => window.innerWidth)
  const [sidebarChoice, setSidebarChoice] = useState<SidebarChoice>(readSidebarChoice)
  const [railChoice, setRailChoice] = useState<boolean | null>(null)
  const [paletteOpen, setPaletteOpen] = useState(false)

  useEffect(() => {
    const onResize = () => setWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const railDocked = width >= RAIL_DOCK_FROM
  const sidebarCollapsed = sidebarChoice ? sidebarChoice === 'collapsed' : width < SIDEBAR_AUTO_COLLAPSE_BELOW
  const railOpen = railChoice ?? railDocked

  const toggleSidebar = useCallback(() => {
    const next: SidebarChoice = sidebarCollapsed ? 'expanded' : 'collapsed'
    setSidebarChoice(next)
    try {
      localStorage.setItem(SIDEBAR_STORAGE_KEY, next)
    } catch {
      // storage unavailable: the choice lasts for this page view only
    }
  }, [sidebarCollapsed])

  const toggleRail = useCallback(() => setRailChoice(!railOpen), [railOpen])
  const closeRail = useCallback(() => setRailChoice(false), [])
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

  // As an overlay the rail closes on Escape (the palette handles its own Escape).
  const overlayOpen = railOpen && !railDocked
  useEffect(() => {
    if (!overlayOpen || paletteOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeRail()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [overlayOpen, paletteOpen, closeRail])

  const mainWidth = width - (sidebarCollapsed ? SIDEBAR_NARROW_PX : SIDEBAR_WIDE_PX) - (railOpen && railDocked ? RAIL_PX : 0)
  const value = useMemo<ShellStateValue>(
    () => ({
      width,
      sidebarCollapsed,
      toggleSidebar,
      railOpen,
      railDocked,
      toggleRail,
      closeRail,
      paletteOpen,
      openPalette,
      closePalette,
      mainWidth,
      showPulse: mainWidth >= PULSE_MIN_MAIN_PX,
    }),
    [width, sidebarCollapsed, toggleSidebar, railOpen, railDocked, toggleRail, closeRail, paletteOpen, openPalette, closePalette, mainWidth],
  )
  return <ShellStateContext.Provider value={value}>{children}</ShellStateContext.Provider>
}

export function useShellState(): ShellStateValue {
  const context = useContext(ShellStateContext)
  if (!context) throw new Error('useShellState must be used inside ShellStateProvider')
  return context
}
