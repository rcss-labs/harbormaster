import { useSSOConfig } from '../../../api/hooks'
import styles from './sso.module.css'
import { IdPCard } from './IdPCard'
import { OptionsCard } from './OptionsCard'
import { SPCard } from './SPCard'
import { SetupChecklist } from './SetupChecklist'
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
      <SetupChecklist config={config.data} />
      <div id="sso-on" className={styles.anchor}>
        <StatusCard config={config.data} />
      </div>
      <div id="sso-idp" className={styles.anchor}>
        <IdPCard config={config.data} />
      </div>
      <div id="sso-sp" className={styles.anchor}>
        <SPCard config={config.data} />
      </div>
      <div id="sso-options" className={styles.anchor}>
        <OptionsCard config={config.data} />
      </div>
      <div id="sso-test" className={styles.anchor}>
        <TestCard config={config.data} />
      </div>
    </div>
  )
}
