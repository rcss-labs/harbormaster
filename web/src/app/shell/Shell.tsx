import { useEffect, useLayoutEffect, useRef } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { EmptyState } from '../../components/EmptyState'
import { usePhone } from '../../components/useNarrow'
import { useVisualViewport } from '../../components/useVisualViewport'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { ActivityRail } from './ActivityRail'
import { BottomTabBar } from './BottomTabBar'
import { CommandPalette } from './CommandPalette'
import { CrumbsProvider } from './crumbs'
import { KeyboardHelp } from './KeyboardHelp'
import { MobileTopBar } from './MobileTopBar'
import { ShellStateProvider, useShellState } from './ShellState'
import { useShortcuts } from './shortcuts'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'
import styles from './shell.module.css'

/** Marks the document as phone-sized for global CSS and tracks the visual viewport so the keyboard never hides the bottom bars. */
function PhoneEnvironment() {
  useVisualViewport()
  // A layout effect: the attribute drives the bottom inset in CSS, so it must be set in the commit that first paints the
  // phone shell (a passive effect left a frame with the desktop inset, and could still be pending when a test read it).
  useLayoutEffect(() => {
    document.documentElement.dataset.hmPhone = '1'
    return () => {
      delete document.documentElement.dataset.hmPhone
    }
  }, [])
  return null
}

/** Layout for every signed-in screen except /password: sidebar, top bar, activity drawer, command palette, shortcut help and the no-access guard. */
export function Shell() {
  return (
    <ShellStateProvider>
      <CrumbsProvider>
        <ShellFrame />
      </CrumbsProvider>
    </ShellStateProvider>
  )
}

function ShellFrame() {
  const { me, refresh } = useAuth()
  const { pathname, search, hash } = useLocation()
  const navigate = useNavigate()
  const phone = usePhone()
  const toast = useToast()
  const linkResultHandled = useRef(false)

  // The SSO link flow returns to /?sso_link=...; report it once, then clean the URL.
  useEffect(() => {
    if (linkResultHandled.current) return
    const params = new URLSearchParams(search)
    const result = params.get('sso_link')
    if (result === null) return
    linkResultHandled.current = true
    if (result === 'ok') {
      toast.show('Your SSO account is linked.', 'ok')
      void refresh()
    } else if (result === 'sso_identity_taken') {
      toast.show('That SSO identity is already linked to another user.', 'bad')
    } else {
      toast.show('Linking failed. Ask a global admin to check the audit log.', 'bad')
    }
    params.delete('sso_link')
    const rest = params.toString()
    navigate({ pathname, search: rest ? `?${rest}` : '', hash }, { replace: true })
  }, [search, hash, pathname, navigate, refresh, toast])
  const noAccess = !!me && !me.admin && Object.keys(me.perms).length === 0 && pathname !== '/me/access' && pathname !== '/more'
  const { sidebarCollapsed, paletteOpen, helpOpen, openHelp, closeHelp } = useShellState()
  useShortcuts(navigate, { paletteOpen, openHelp })
  const guarded = noAccess ? <EmptyState>You don't have access to anything yet. Ask a global admin.</EmptyState> : <Outlet />
  // One tree for both layouts: the page's <main> keeps its place when a phone rotates across the breakpoint, so a terminal session,
  // an unsaved deploy change or a scroll position survive (two different trees would remount the whole page).
  // On phones the sidebar, the density and help buttons and the shortcut help are desktop-only; More carries what they held.
  const classes = phone ? styles.appPhone : [styles.app, sidebarCollapsed ? styles.appCollapsed : ''].join(' ')
  return (
    <div className={classes}>
      {phone ? <PhoneEnvironment /> : <Sidebar />}
      <div className={phone ? styles.stagePhone : styles.main}>
        {phone ? <MobileTopBar /> : <TopBar />}
        <main className={phone ? `${styles.content} ${styles.contentPhone}` : styles.content} data-hm-phone={phone ? '' : undefined}>
          {guarded}
        </main>
        {phone && <BottomTabBar />}
      </div>
      <ActivityRail />
      {paletteOpen && <CommandPalette />}
      {!phone && helpOpen && <KeyboardHelp onClose={closeHelp} />}
    </div>
  )
}
