import type { Targets } from './targets'

/** Every route the floci route passes (desktop widths and the phone pass) load; ids are resolved from the live API. */
export interface RouteSpec {
  name: string
  path: (targets: Targets) => string | null
}

export const SERVICE_TABS = ['overview', 'tasks', 'logs', 'metrics', 'terminal', 'deploy', 'task-definition', 'scaling', 'operations']
export const ANALYTICS_TABS = ['delivery', 'reliability', 'capacity', 'operations']
export const SETTINGS_TABS = ['general', 'accounts', 'users', 'tags', 'sso', 'audit']

export const ROUTES: RouteSpec[] = [
  { name: 'overview', path: () => '/' },
  { name: 'services', path: (t) => (t.hasServicesApi ? '/services' : null) },
  { name: 'clusters', path: () => '/clusters' },
  { name: 'cluster', path: (t) => `/clusters/${t.clusterId}` },
  ...SERVICE_TABS.map((tab) => ({ name: `service-${tab}`, path: (t: Targets) => `/services/${t.serviceId}/${tab}` })),
  { name: 'tasks', path: () => '/tasks' },
  { name: 'task-terminal', path: (t) => (t.standaloneTaskShortId ? `/tasks/${t.standaloneTaskShortId}/terminal` : null) },
  { name: 'networks', path: () => '/networks' },
  { name: 'storage', path: () => '/storage' },
  ...ANALYTICS_TABS.map((tab) => ({ name: `analytics-${tab}`, path: () => `/analytics/${tab}` })),
  ...SETTINGS_TABS.map((tab) => ({ name: `settings-${tab}`, path: () => `/settings/${tab}` })),
  { name: 'my-access', path: () => '/me/access' },
]
