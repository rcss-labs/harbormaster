import { useState, type ReactNode } from 'react'
import { Link, Navigate } from 'react-router'
import { useAuthOptions, useClusters } from '../../api/hooks'
import { Icon } from '../../components/Icon'
import type { IconName } from '../../components/icons'
import { Pill } from '../../components/Pill'
import { StatusDot } from '../../components/StatusDot'
import { useToast } from '../../components/Toast'
import { usePhone } from '../../components/usePhone'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import { LinkSSODialog } from '../shell/LinkSSODialog'
import { usePins } from '../shell/pins'
import { missingPinHint, pinStatus } from '../shell/pinStatus'
import { useShellCounts } from '../shell/useShellCounts'
import { useShellState } from '../shell/ShellState'
import { toggleTheme, useIsDark } from '../theme'
import styles from './more.module.css'

interface RowProps {
  icon?: IconName
  label: string
  /** Text or a pill at the right, before the chevron. */
  right?: ReactNode
  to?: string
  onClick?: () => void
  danger?: boolean
  pressed?: boolean
  hasDialog?: boolean
}

/** One 52 px row: a router link when `to` is set, otherwise a button. */
function Row({ icon, label, right, to, onClick, danger = false, pressed, hasDialog = false }: RowProps) {
  const className = `${styles.row} ${danger ? styles.danger : ''}`.trim()
  const content = (
    <>
      {icon && <Icon name={icon} size={18} />}
      <span className={styles.label}>{label}</span>
      {(right !== undefined || !danger) && (
        <span className={styles.right}>
          {right}
          {!danger && <Icon name="chev" size={16} />}
        </span>
      )}
    </>
  )
  if (to !== undefined) {
    return (
      <Link to={to} className={className}>
        {content}
      </Link>
    )
  }
  return (
    <button type="button" className={className} onClick={onClick} aria-pressed={pressed} aria-haspopup={hasDialog ? 'dialog' : undefined}>
      {content}
    </button>
  )
}

function Panel({ label, heading, children }: { label: string; heading?: string; children: ReactNode }) {
  return (
    <>
      {heading && <div className={styles.heading}>{heading}</div>}
      <section className={styles.panel} aria-label={label}>
        {children}
      </section>
    </>
  )
}

const plural = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`

/**
 * The phone's replacement for the sidebar and the top bar's right-hand buttons: account, activity, infrastructure,
 * insights, admin or account pages, pins, theme and sign out. Desktop has no More page.
 */
export function MorePage() {
  const phone = usePhone()
  const { me, logout } = useAuth()
  const toast = useToast()
  const { toggleRail } = useShellState()
  const counts = useShellCounts()
  const authOptions = useAuthOptions()
  const allClusters = useClusters()
  const { pins, remove } = usePins()
  const dark = useIsDark()
  const [linking, setLinking] = useState(false)
  useCrumbs([['More']])
  if (!phone) return <Navigate to="/" replace />
  if (!me) return null

  const sso = authOptions.data?.sso
  const canLinkSSO = sso != null && me.has_password && !me.sso_linked
  const methods = [me.has_password ? 'Password' : null, me.sso_linked ? 'SSO' : null, me.break_glass ? 'Break-glass' : null].filter((m) => m !== null)
  const identity = [me.email, methods.join(', ')].filter((part) => part).join(' · ')
  const signOut = async () => {
    try {
      await logout()
    } catch {
      toast.show('Sign out failed on the server. You were signed out in this browser.', 'bad')
    }
  }

  return (
    <div className={styles.page}>
      <Panel label="Signed in">
        <div className={`${styles.row} ${styles.static}`}>
          <span className={styles.avatar} aria-hidden="true">
            {me.username.slice(0, 2).toUpperCase()}
          </span>
          <span className={styles.who}>
            <span>
              Signed in as <b>{me.username}</b> · {me.admin ? 'Global admin' : 'User'}
            </span>
            {identity && <span className={styles.sub}>{identity}</span>}
          </span>
        </div>
        <Row
          icon="activity"
          label="Activity"
          hasDialog
          onClick={toggleRail}
          right={counts.problems > 0 ? <Pill kind="bad">{plural(counts.problems, 'problem', 'problems')}</Pill> : undefined}
        />
      </Panel>

      <Panel label="Infrastructure" heading="Infrastructure">
        <Row icon="networks" label="Networks" to="/networks" />
        <Row icon="storage" label="Storage" to="/storage" />
      </Panel>

      <Panel label="Insights" heading="Insights">
        <Row icon="analytics" label="Analytics" to="/analytics/delivery" />
        <Row icon="logs" label="Log search" to="/logs" />
      </Panel>

      {me.admin && (
        <Panel label="Admin" heading="Admin">
          <Row icon="settings" label="General" to="/settings/general" />
          <Row
            icon="globe"
            label="AWS accounts"
            to="/settings/accounts"
            right={counts.accountIssues > 0 ? <Pill kind="warn">{`${counts.accountIssues} account issues`}</Pill> : undefined}
          />
          <Row icon="user" label="Users & permissions" to="/settings/users" />
          <Row icon="tag" label="Tags" to="/settings/tags" />
          <Row icon="shield" label="Single sign-on" to="/settings/sso" right={authOptions.data ? (sso?.enabled ? 'On' : 'Off') : undefined} />
          <Row icon="history" label="Audit log" to="/settings/audit" />
        </Panel>
      )}

      <Panel label="Pinned" heading="Pinned">
        {pins.length === 0 && <div className={styles.empty}>Pin a service or cluster from its page.</div>}
        {pins.map((pin) => {
          const status = pinStatus(pin, allClusters.data?.items)
          const detail = pin.kind === 'cluster' ? `${pin.account_name} · ${pin.account_id}` : pin.cluster
          const label = `${pin.name} · ${status.text} · ${detail}${status.missing ? ` · ${missingPinHint}` : ''}`
          return (
            <div key={`${pin.kind}:${pin.id}`} className={styles.pinRow}>
              <Link
                to={pin.kind === 'cluster' ? `/clusters/${pin.id}` : `/services/${pin.id}/overview`}
                className={styles.row}
                aria-label={label}
              >
                <StatusDot bare kind={status.kind} />
                <span className={`${styles.label} ${styles.mono}`}>{pin.name}</span>
                <span className={styles.right}>
                  <span className={styles.detail}>{detail}</span>
                  <Icon name="chev" size={16} />
                </span>
              </Link>
              {status.missing && (
                <button
                  type="button"
                  className={styles.pinRemove}
                  aria-label={`Remove pin ${pin.name}`}
                  title="Remove from pinned"
                  onClick={() => remove(pin.kind, pin.id)}
                >
                  ×
                </button>
              )}
            </div>
          )
        })}
      </Panel>

      <Panel label="Account" heading="Account">
        <Row icon="key" label="My access" to="/me/access" />
        {me.has_password && <Row icon="lock" label="Change password" to="/password" />}
        {canLinkSSO && <Row icon="shield" label="Link SSO account" hasDialog right="Not linked" onClick={() => setLinking(true)} />}
        <Row icon="sun" label="Theme" pressed={dark} right={dark ? 'Dark' : 'Light'} onClick={toggleTheme} />
      </Panel>

      <Panel label="Sign out">
        <Row icon="ext" label="Sign out" danger onClick={signOut} />
      </Panel>

      <p className={styles.footer}>Harbormaster · Running on ECS · harbormaster/harbormaster-server</p>
      {linking && <LinkSSODialog onClose={() => setLinking(false)} />}
    </div>
  )
}
