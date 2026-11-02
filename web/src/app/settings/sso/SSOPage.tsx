import { useSSOConfig } from '../../../api/hooks'
import styles from './sso.module.css'
import { IdPCard } from './IdPCard'
import { OptionsCard } from './OptionsCard'
import { SPCard } from './SPCard'
import { StatusCard } from './StatusCard'
import { TestCard } from './TestCard'

/** Settings → Single sign-on (admin only; the tab gate lives in SettingsPage). */
export function SSOPage() {
  const config = useSSOConfig()
  if (config.isPending) return <p className={styles.hint}>Loading...</p>
  if (config.isError) {
    return (
      <p role="alert" className={styles.err}>
        {config.error.message}
      </p>
    )
  }
  return (
    <div className={styles.stack}>
      <StatusCard config={config.data} />
      <IdPCard config={config.data} />
      <SPCard config={config.data} />
      <OptionsCard config={config.data} />
      <TestCard config={config.data} />
    </div>
  )
}
