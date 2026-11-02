import { useState } from 'react'
import { Navigate, useParams } from 'react-router'
import { AdminOnly } from '../../components/AdminOnly'
import { Tabs } from '../../components/Tabs'
import { Accounts } from '../admin/accounts/Accounts'
import { Audit } from '../admin/audit/Audit'
import { Tags } from '../admin/tags/Tags'
import { Users } from '../admin/users/Users'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import { HeaderActionSlot } from './HeaderAction'
import { SSOPage } from './sso/SSOPage'
import { StoppedTasksCard } from './StoppedTasksCard'
import styles from './settings.module.css'

export type SettingsTab = 'general' | 'accounts' | 'users' | 'tags' | 'sso' | 'audit'

export const SETTINGS_TABS: [SettingsTab, string][] = [
  ['general', 'General'],
  ['accounts', 'AWS accounts'],
  ['users', 'Users & permissions'],
  ['tags', 'Tags'],
  ['sso', 'Single sign-on'],
  ['audit', 'Audit log'],
]

const isSettingsTab = (value: string | undefined): value is SettingsTab => SETTINGS_TABS.some(([key]) => key === value)

function TabPanel({ tab }: { tab: SettingsTab }) {
  switch (tab) {
    case 'general':
      return <StoppedTasksCard />
    case 'tags':
      return <Tags />
    case 'accounts':
      return <Accounts />
    case 'users':
      return <Users />
    case 'sso':
      return <SSOPage />
    case 'audit':
      return <Audit />
  }
}

export function SettingsPage() {
  const { tab } = useParams()
  const { me } = useAuth()
  const [actionSlot, setActionSlot] = useState<HTMLElement | null>(null)
  const activeTab = isSettingsTab(tab) ? tab : null
  const tabLabel = SETTINGS_TABS.find(([key]) => key === activeTab)?.[1]
  useCrumbs(tabLabel ? [['Settings', '/settings/general'], [tabLabel]] : [['Settings']])

  if (!activeTab) return <Navigate to="/settings/general" replace />

  // Tab keys are AdminOnly area names. Non-admins get the explanation instead of the page, heading and tabs included.
  if (!me?.admin) return <AdminOnly area={activeTab} />

  return (
    <HeaderActionSlot.Provider value={actionSlot}>
      <div className={styles.sticky}>
        <div className={styles.pageHead}>
          <h1>Settings</h1>
          <div role="group" aria-label="Tab actions" className={styles.actions} ref={setActionSlot} />
        </div>
        <Tabs items={SETTINGS_TABS.map(([key, label]) => ({ key, label }))} active={activeTab} hrefFor={(key) => `/settings/${key}`} />
      </div>
      <div className={styles.panel}>
        <TabPanel tab={activeTab} />
      </div>
    </HeaderActionSlot.Provider>
  )
}
