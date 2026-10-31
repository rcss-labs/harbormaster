import { useQueryClient } from '@tanstack/react-query'
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { api, ApiError, setCsrfToken } from '../../api/client'
import type { MeDTO } from '../../api/types'

interface AuthState {
  me: MeDTO | null
  loading: boolean
  /** Refetches GET /api/me (after login, setup or a password change). */
  refresh: () => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [me, setMe] = useState<MeDTO | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const next = await api.get<MeDTO>('/api/me')
      setCsrfToken(next.csrf_token)
      setMe(next)
    } catch (error) {
      // 401 simply means "not signed in"; anything else is also treated as signed out rather than crashing the tree.
      if (!(error instanceof ApiError) || error.status !== 401) console.error(error)
      setMe(null)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial session probe
    void refresh()
  }, [refresh])

  useEffect(() => {
    const onUnauthenticated = () => setMe(null)
    window.addEventListener('hm:unauthenticated', onUnauthenticated)
    return () => window.removeEventListener('hm:unauthenticated', onUnauthenticated)
  }, [])

  const logout = useCallback(async () => {
    await api.post('/api/auth/logout')
    setMe(null)
    queryClient.clear()
    navigate('/login')
  }, [navigate, queryClient])

  const value = useMemo(() => ({ me, loading, refresh, logout }), [me, loading, refresh, logout])
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const context = useContext(AuthContext)
  if (!context) throw new Error('useAuth must be used inside AuthProvider')
  return context
}
