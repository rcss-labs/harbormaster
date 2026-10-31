import { useAuth } from '../auth/AuthProvider'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

export function Filters() {
  const { me } = useAuth()
  const { filters, setAccount, setRegion } = useFilters()
  if (!me) return null
  return (
    <>
      <select className={styles.ctl} aria-label="Account filter" value={filters.account ?? ''} onChange={(e) => setAccount(e.target.value || undefined)}>
        <option value="">All accounts</option>
        {me.accounts.map((account) => (
          <option key={account.account_id} value={account.account_id}>
            {account.name}
          </option>
        ))}
      </select>
      <select className={styles.ctl} aria-label="Region filter" value={filters.region ?? ''} onChange={(e) => setRegion(e.target.value || undefined)}>
        <option value="">All regions</option>
        {me.regions.map((region) => (
          <option key={region} value={region}>
            {region}
          </option>
        ))}
      </select>
    </>
  )
}
