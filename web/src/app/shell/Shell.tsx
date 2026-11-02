import { useEffect, useRef } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { EmptyState } from '../../components/EmptyState'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { ActivityRail } from './ActivityRail'
import { CommandPalette } from './CommandPalette'
import { CrumbsProvider } from './crumbs'
import { PollerBanners } from './PollerBanners'
import { ShellStateProvider, useShellState } from './ShellState'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'
import styles from './shell.module.css'

/** Layout for every signed-in screen except /password: sidebar, top bar, activity rail, command palette, poller banners and the no-access guard. */
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
  const noAccess = !!me && !me.admin && Object.keys(me.perms).length === 0 && pathname !== '/me/access'
  const { sidebarCollapsed, railOpen, railDocked, paletteOpen } = useShellState()
  const classes = [styles.app, sidebarCollapsed ? styles.appCollapsed : '', railOpen && railDocked ? styles.appDocked : ''].join(' ')
  return (
    <div className={classes}>
      <Sidebar />
      <div className={styles.main}>
        <TopBar />
        <main className={styles.content}>
          <PollerBanners />
          {noAccess ? <EmptyState>You don't have access to anything yet. Ask a global admin.</EmptyState> : <Outlet />}
        </main>
      </div>
      <ActivityRail />
      {paletteOpen && <CommandPalette />}
    </div>
  )
}
