import { Navigate, useParams } from 'react-router'
import { AdminOnly, type AdminArea } from '../../components/AdminOnly'
import { Tabs } from '../../components/Tabs'
import { Accounts } from '../admin/accounts/Accounts'
import { Audit } from '../admin/audit/Audit'
import { Tags } from '../admin/tags/Tags'
import { Users } from '../admin/users/Users'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import styles from './settings.module.css'

export type SettingsTab = 'general' | 'accounts' | 'users' | 'audit'

export const SETTINGS_TABS: [SettingsTab, string][] = [
  ['general', 'General'],
  ['accounts', 'AWS accounts'],
  ['users', 'Users & permissions'],
  ['audit', 'Audit log'],
]

/** The admin area each tab belongs to, for the non-admin explanation. */
const AREA_FOR_TAB: Record<SettingsTab, AdminArea> = {
  general: 'general',
  accounts: 'accounts',
  users: 'users',
  audit: 'audit',
}

const isSettingsTab = (value: string | undefined): value is SettingsTab => SETTINGS_TABS.some(([key]) => key === value)

function TabPanel({ tab }: { tab: SettingsTab }) {
  switch (tab) {
    case 'general':
      // Task 13 adds the stopped-tasks card under <Tags />.
      return <Tags />
    case 'accounts':
      return <Accounts />
    case 'users':
      return <Users />
    case 'audit':
      return <Audit />
  }
}

export function SettingsPage() {
  const { tab } = useParams()
  const { me } = useAuth()
  const activeTab = isSettingsTab(tab) ? tab : null
  const tabLabel = SETTINGS_TABS.find(([key]) => key === activeTab)?.[1]
  useCrumbs(tabLabel ? [['Settings', '/settings/general'], [tabLabel]] : [['Settings']])

  if (!activeTab) return <Navigate to="/settings/general" replace />

  return (
    <>
      <div className={styles.pageHead}>
        <h1>Settings</h1>
      </div>
      <Tabs
        items={SETTINGS_TABS.map(([key, label]) => ({ key, label }))}
        active={activeTab}
        hrefFor={(key) => `/settings/${key}`}
      />
      <div className={styles.panel}>{me?.admin ? <TabPanel tab={activeTab} /> : <AdminOnly area={AREA_FOR_TAB[activeTab]} />}</div>
    </>
  )
}
