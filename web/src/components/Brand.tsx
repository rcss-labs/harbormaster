import { useIsDark } from '../app/theme'
import styles from './Brand.module.css'

const SYMBOL_LIGHT = '/brand/harbormaster-symbol.svg'
const SYMBOL_DARK = '/brand/harbormaster-symbol-dark.svg'
const LOCKUP_LIGHT = '/brand/harbormaster-lockup.png'

/**
 * Theme-aware brand mark. No dark lockup exists (design §14.1), so the sign-in
 * variant falls back to the dark symbol plus the wordmark in the UI font.
 */
export function Brand({ variant }: { variant: 'sidebar' | 'signin' }) {
  const dark = useIsDark()
  if (variant === 'signin' && !dark) {
    return <img className={styles.lockup} src={LOCKUP_LIGHT} alt="Harbormaster" width={543} height={181} />
  }
  return (
    <div className={styles.mark}>
      <img className={variant === 'signin' ? styles.symbolLarge : styles.symbol} src={dark ? SYMBOL_DARK : SYMBOL_LIGHT} alt="Harbormaster" />
      <span className={styles.wordmark}>Harbormaster</span>
    </div>
  )
}
