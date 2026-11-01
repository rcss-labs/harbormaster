import { useQuery } from '@tanstack/react-query'
import { Navigate, Outlet, Route, Routes, useLocation } from 'react-router'
import { api } from '../api/client'
import { ChangePassword } from './auth/ChangePassword'
import { Login } from './auth/Login'
import { Setup } from './auth/Setup'
import { Accounts } from './admin/accounts/Accounts'
import { Audit } from './admin/audit/Audit'
import { Tags } from './admin/tags/Tags'
import { Users } from './admin/users/Users'
import { useAuth } from './auth/AuthProvider'
import { Cluster } from './clusters/Cluster'
import { Clusters } from './clusters/Clusters'
import { NetworkRegionPage } from './networks/NetworkRegionPage'
import { NetworkVpcPage } from './networks/NetworkVpcPage'
import { NetworksPage } from './networks/NetworksPage'
import { Overview } from './overview/Overview'
import { ServicePage } from './services/ServicePage'
import { Shell } from './shell/Shell'
import { MyAccess } from './me/MyAccess'
import { StoragePage } from './storage/StoragePage'
import { Tasks } from './tasks/Tasks'
import { TaskTerminal } from './tasks/TaskTerminal'
import styles from './auth/auth.module.css'

const Loading = () => <p className={styles.loading}>Loading…</p>

/** Signed-out visitors go to /setup on a fresh install, otherwise /login, remembering where they were headed. */
function RedirectToSignIn() {
  const location = useLocation()
  const setup = useQuery({
    queryKey: ['auth-setup'],
    queryFn: () => api.get<{ needed: boolean }>('/api/auth/setup'),
    retry: false,
  })
  if (setup.isPending) return <Loading />
  const from = location.pathname + location.search
  return <Navigate to={setup.data?.needed ? '/setup' : '/login'} state={{ from }} replace />
}

/** Layout route guarding the authenticated area: sign-in required, and a forced password change blocks everything else. */
function RequireAuth() {
  const { me, loading } = useAuth()
  const location = useLocation()
  if (loading) return <Loading />
  if (!me) return <RedirectToSignIn />
  if (me.must_change_password && location.pathname !== '/password') return <Navigate to="/password" replace />
  return <Outlet />
}

/** Login and setup are for signed-out visitors; a signed-in user is sent on to the app. */
function PublicOnly() {
  const { me, loading } = useAuth()
  const location = useLocation()
  if (loading) return <Loading />
  // Once sign-in succeeds `me` appears here first, so honour the remembered path rather than racing Login's own navigate.
  if (me) return <Navigate to={(location.state as { from?: string } | null)?.from ?? '/'} replace />
  return <Outlet />
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<PublicOnly />}>
        <Route path="login" element={<Login />} />
        <Route path="setup" element={<Setup />} />
      </Route>
      <Route element={<RequireAuth />}>
        <Route path="password" element={<ChangePassword />} />
        <Route element={<Shell />}>
          <Route index element={<Overview />} />
          <Route path="clusters" element={<Clusters />} />
          <Route path="clusters/:clusterId" element={<Cluster />} />
          <Route path="services/:serviceId/:tab?" element={<ServicePage />} />
          <Route path="tasks" element={<Tasks />} />
          <Route path="tasks/:shortId/terminal" element={<TaskTerminal />} />
          <Route path="admin/users" element={<Users />} />
          <Route path="admin/tags" element={<Tags />} />
          <Route path="admin/accounts" element={<Accounts />} />
          <Route path="admin/audit" element={<Audit />} />
          <Route path="me/access" element={<MyAccess />} />
          {/* plan 05: networks and storage */}
          <Route path="networks" element={<NetworksPage />} />
          <Route path="networks/:accountId/:region" element={<NetworkRegionPage />} />
          <Route path="networks/:accountId/:region/:vpcId" element={<NetworkVpcPage />} />
          <Route path="storage" element={<StoragePage />} />
          <Route path="*" element={<p>Page not found.</p>} />
        </Route>
      </Route>
    </Routes>
  )
}
