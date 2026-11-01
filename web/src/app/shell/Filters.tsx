import { useAuth } from '../auth/AuthProvider'
import { MultiSelect } from '../../components/MultiSelect'
import { useFilters, visibleTagIds } from './useFilters'
import styles from './shell.module.css'

export function Filters({ variant = 'topbar' }: { variant?: 'topbar' | 'page' }) {
  const { me } = useAuth()
  const { filters, setAccount, setRegion, setTags } = useFilters()
  if (!me) return null
  const page = variant === 'page'
  const tagOptions = visibleTagIds(me).flatMap((id) => {
    const tag = me.tags.find((t) => t.id === id)
    return tag ? [{ value: String(id), label: tag.name }] : []
  })
  return (
    <>
      <select className={styles.ctl} aria-label={page ? 'Account' : 'Account filter'} value={filters.account ?? ''} onChange={(e) => setAccount(e.target.value || undefined)}>
        <option value="">All accounts</option>
        {me.accounts.map((account) => (
          <option key={account.account_id} value={account.account_id}>
            {account.name}
          </option>
        ))}
      </select>
      <select className={styles.ctl} aria-label={page ? 'Region' : 'Region filter'} value={filters.region ?? ''} onChange={(e) => setRegion(e.target.value || undefined)}>
        <option value="">All regions</option>
        {me.regions.map((region) => (
          <option key={region} value={region}>
            {region}
          </option>
        ))}
      </select>
      <MultiSelect
        label={page ? 'Tags' : 'Tag filter'}
        placeholder="All tags"
        options={tagOptions}
        value={(filters.tag ?? []).map(String)}
        onChange={(values) => setTags(values.map(Number))}
      />
    </>
  )
}
