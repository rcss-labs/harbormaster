import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { SSOConfigDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Pill } from '../../../components/Pill'
import { useToast } from '../../../components/Toast'
import { useAuth } from '../../auth/AuthProvider'
import styles from './sso.module.css'

const GUARD_TITLE =
  'Flag at least one global admin as break-glass before turning on single sign-on. Create one with harbormaster create-admin, or flag an existing admin who has a password.'

function enableSub(blocked: number): string {
  if (blocked === 0) return 'Only break-glass accounts can sign in with a password while single sign-on is on.'
  return `${blocked} users have a password but are not break-glass; they can only sign in with SSO while it is on. Passwords are kept and work again if you turn it off.`
}

export function StatusCard({ config }: { config: SSOConfigDTO }) {
  const { me } = useAuth()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [confirming, setConfirming] = useState(false)

  const setEnabled = useMutation({
    mutationFn: (enabled: boolean) => api.put<SSOConfigDTO>('/api/sso/enabled', { enabled }),
    onSuccess: (updated) => {
      queryClient.setQueryData(['sso'], updated)
      void queryClient.invalidateQueries({ queryKey: ['auth-options'] })
    },
    onError: (error) => toast.show(error.message, 'bad'),
  })

  const turningOn = !config.enabled
  const guarded = turningOn && config.break_glass_admins === 0

  return (
    <Card title="Status" right={<Pill kind={config.enabled ? 'ok' : 'mute'}>{config.enabled ? 'SSO on' : 'SSO off'}</Pill>}>
      <div className={styles.stack}>
        {config.warnings.map((warning) => (
          <Banner key={warning} kind="warn">
            {warning}
          </Banner>
        ))}
        <div className={styles.row}>
          <button
            type="button"
            className={`${styles.btn} ${turningOn ? styles.primary : ''}`}
            disabled={guarded || setEnabled.isPending}
            title={guarded ? GUARD_TITLE : undefined}
            onClick={() => setConfirming(true)}
          >
            {turningOn ? 'Turn on single sign-on' : 'Turn off single sign-on'}
          </button>
        </div>
        {me && !me.sso_linked && (
          <p className={styles.hint}>
            Link your own account first so you can sign in with SSO: use Link SSO account in the sidebar.
          </p>
        )}
      </div>
      {confirming && (
        <ConfirmDialog
          title={turningOn ? 'Turn on single sign-on?' : 'Turn off single sign-on?'}
          sub={
            turningOn
              ? enableSub(config.password_users_blocked_on_enable)
              : 'Everyone signed in with SSO is signed out now. Users with a password can sign in with it again; users without one need an admin to set one.'
          }
          confirmLabel={turningOn ? 'Turn on' : 'Turn off'}
          danger={!turningOn}
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            setConfirming(false)
            setEnabled.mutate(turningOn)
          }}
        />
      )}
    </Card>
  )
}
