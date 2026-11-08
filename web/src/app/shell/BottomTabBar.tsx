import { Link, useLocation } from 'react-router'
import { Icon } from '../../components/Icon'
import type { IconName } from '../../components/icons'
import { useAuth } from '../auth/AuthProvider'
import { useShellCounts } from './useShellCounts'
import styles from './shell.module.css'

/** Paths that belong under the More tab, so it stays current on the pages it leads to. */
const MORE_PREFIXES = ['/more', '/networks', '/storage', '/analytics', '/logs', '/settings', '/me', '/password']

const under = (pathname: string, prefix: string) => pathname === prefix || pathname.startsWith(`${prefix}/`)

interface Tab {
  to: string
  label: string
  icon: IconName
  current: boolean
  badge?: { count: number; kind: 'bad' | 'warn'; noun: string }
}

const cap = (count: number) => (count > 99 ? '99+' : String(count))

/** Phone navigation: five tabs fixed to the bottom edge. The page keeps clear of it through `--hm-bottom-inset`. */
export function BottomTabBar() {
  const { me } = useAuth()
  const { pathname } = useLocation()
  const counts = useShellCounts()
  const tabs: Tab[] = [
    {
      to: '/',
      label: 'Overview',
      icon: 'overview',
      current: pathname === '/',
      badge: counts.problems > 0 ? { count: counts.problems, kind: 'bad', noun: 'need attention' } : undefined,
    },
    { to: '/services', label: 'Services', icon: 'services', current: under(pathname, '/services') },
    { to: '/clusters', label: 'Clusters', icon: 'clusters', current: under(pathname, '/clusters') },
    {
      to: '/tasks',
      label: 'Tasks',
      icon: 'tasks',
      current: under(pathname, '/tasks'),
      badge: counts.tasks ? { ...counts.tasks, noun: 'problems' } : undefined,
    },
    {
      to: '/more',
      label: 'More',
      icon: 'more',
      current: MORE_PREFIXES.some((prefix) => under(pathname, prefix)),
      badge: me?.admin && counts.accountIssues > 0 ? { count: counts.accountIssues, kind: 'warn', noun: 'account issues' } : undefined,
    },
  ]
  return (
    <nav className={styles.tabbar} aria-label="Main">
      {tabs.map((tab) => (
        <Link
          key={tab.to}
          to={tab.to}
          className={styles.tab}
          aria-label={tab.badge ? `${tab.label}, ${tab.badge.count} ${tab.badge.noun}` : tab.label}
          aria-current={tab.current ? 'page' : undefined}
        >
          <span className={styles.tabIcon}>
            <Icon name={tab.icon} size={18} />
            {tab.badge && <span className={`${styles.tabBadge} ${tab.badge.kind === 'bad' ? styles.tabBadgeBad : styles.tabBadgeWarn}`}>{cap(tab.badge.count)}</span>}
          </span>
          <span className={styles.tabLabel}>{tab.label}</span>
        </Link>
      ))}
    </nav>
  )
}
