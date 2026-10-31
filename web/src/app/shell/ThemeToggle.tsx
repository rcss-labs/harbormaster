import { toggleTheme } from '../theme'
import styles from './shell.module.css'

export function ThemeToggle() {
  return (
    <button type="button" className={styles.btn} aria-label="Toggle theme" onClick={toggleTheme}>
      ◐
    </button>
  )
}
