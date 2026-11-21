import type { ConfigMatchDTO } from '../../api/configSearch'
import { matchLabel } from './model'
import styles from './configsearch.module.css'

const MAX_CHIPS = 4

/** What matched, as small chips: at most four, then a count of the rest. */
export function MatchChips({ matches, firstContainer }: { matches: ConfigMatchDTO[]; firstContainer: string }) {
  const shown = matches.slice(0, MAX_CHIPS)
  const rest = matches.length - shown.length
  return (
    <span className={styles.chipRow}>
      {shown.map((match, index) => (
        <span key={`${match.field}:${match.container}:${match.value}:${index}`} className={styles.matchChip}>
          {matchLabel(match, firstContainer)}
        </span>
      ))}
      {rest > 0 && (
        <span className={styles.matchMore} title={matches.slice(MAX_CHIPS).map((m) => matchLabel(m, firstContainer)).join(', ')}>
          +{rest}
        </span>
      )}
    </span>
  )
}
