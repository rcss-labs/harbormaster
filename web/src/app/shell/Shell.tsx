import { Outlet, useLocation } from 'react-router'
import { EmptyState } from '../../components/EmptyState'
import { useAuth } from '../auth/AuthProvider'
import { CrumbsProvider } from './crumbs'
import { PollerBanners } from './PollerBanners'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'
import styles from './shell.module.css'

/** Layout for every signed-in screen except /password: sidebar, top bar, poller banners and the no-access guard. */
export function Shell() {
  const { me } = useAuth()
  const { pathname } = useLocation()
  const noAccess = !!me && !me.admin && Object.keys(me.perms).length === 0 && pathname !== '/me/access'
  return (
    <CrumbsProvider>
      <div className={styles.app}>
        <Sidebar />
        <div className={styles.main}>
          <TopBar />
          <main className={styles.content}>
            <PollerBanners />
            {noAccess ? <EmptyState>You don't have access to anything yet. Ask a global admin.</EmptyState> : <Outlet />}
          </main>
        </div>
      </div>
    </CrumbsProvider>
  )
}
