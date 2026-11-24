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
import { AgentSettings } from './agent/AgentSettings'
import { HeaderActionSlot } from './HeaderAction'
import { NotificationsPage } from './notifications/NotificationsPage'
import { OffHoursPage } from './offhours/OffHoursPage'
import { SSOPage } from './sso/SSOPage'
import { StoppedTasksCard } from './StoppedTasksCard'
import styles from './settings.module.css'

export type SettingsTab = 'general' | 'accounts' | 'users' | 'tags' | 'offhours' | 'notifications' | 'sso' | 'agent' | 'audit'

export const SETTINGS_TABS: [SettingsTab, string][] = [
  ['general', 'General'],
  ['accounts', 'AWS accounts'],
  ['users', 'Users & permissions'],
  ['tags', 'Tags'],
  ['offhours', 'Off-hours'],
  ['notifications', 'Notifications'],
  ['sso', 'Single sign-on'],
  ['agent', 'AI agent'],
  ['audit', 'Audit log'],
]

/** The AI agent tab exists only on servers where the agent is switched on. */
const tabsFor = (agentEnabled: boolean) => SETTINGS_TABS.filter(([key]) => key !== 'agent' || agentEnabled)

function TabPanel({ tab }: { tab: SettingsTab }) {
  switch (tab) {
    case 'general':
      return <StoppedTasksCard />
    case 'tags':
      return <Tags />
    case 'offhours':
      return <OffHoursPage />
    case 'notifications':
      return <NotificationsPage />
    case 'accounts':
      return <Accounts />
    case 'users':
      return <Users />
    case 'sso':
      return <SSOPage />
    case 'agent':
      return <AgentSettings />
    case 'audit':
      return <Audit />
  }
}

export function SettingsPage() {
  const { tab } = useParams()
  const { me } = useAuth()
  const [actionSlot, setActionSlot] = useState<HTMLElement | null>(null)
  const visibleTabs = tabsFor(me?.agent_enabled ?? false)
  const activeTab = visibleTabs.find(([key]) => key === tab)?.[0] ?? null
  const tabLabel = visibleTabs.find(([key]) => key === activeTab)?.[1]
  useCrumbs(tabLabel ? [['Settings', '/settings/general'], [tabLabel]] : [['Settings']])

  if (!activeTab) return <Navigate to="/settings/general" replace />

  // Tab keys are AdminOnly area names. Non-admins get the explanation instead of the page, heading and tabs included.
  if (!me?.admin) return <AdminOnly area={activeTab} />

  return (
    <HeaderActionSlot.Provider value={actionSlot}>
      <div className={styles.sticky}>
        <div className={styles.pageHead}>
          <h1 className="hm-phone-title-sr">Settings</h1>
          <div role="group" aria-label="Tab actions" className={styles.actions} ref={setActionSlot} />
        </div>
        <Tabs items={visibleTabs.map(([key, label]) => ({ key, label }))} active={activeTab} hrefFor={(key) => `/settings/${key}`} />
      </div>
      <div className={styles.panel}>
        <TabPanel tab={activeTab} />
      </div>
    </HeaderActionSlot.Provider>
  )
}
