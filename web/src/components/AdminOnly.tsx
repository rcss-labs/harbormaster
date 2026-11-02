import styles from './AdminOnly.module.css'

export type AdminArea = 'users' | 'tags' | 'accounts' | 'sso' | 'audit' | 'general'

const COPY: Record<AdminArea, string> = {
  users: 'Only global admins manage users and permissions.',
  tags: 'Only global admins create tags and assign them.',
  accounts: 'Only global admins manage AWS accounts.',
  sso: 'Only global admins change single sign-on.',
  audit: 'Only global admins can read the audit log.',
  general: 'Only global admins change settings.',
}

export function AdminOnly({ area }: { area: AdminArea }) {
  return (
    <div className={styles.panel}>
      <h2>No access</h2>
      <p>{COPY[area]}</p>
    </div>
  )
}
