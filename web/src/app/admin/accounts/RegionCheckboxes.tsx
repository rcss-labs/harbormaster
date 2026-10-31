import { COMMERCIAL_REGIONS } from './accountApi'
import styles from './accounts.module.css'

interface RegionCheckboxesProps {
  selected: string[]
  onChange: (regions: string[]) => void
}

/** Checkbox row for the regions to scan, in the fixed commercial-region order. */
export function RegionCheckboxes({ selected, onChange }: RegionCheckboxesProps) {
  const toggle = (region: string) =>
    onChange(selected.includes(region) ? selected.filter((item) => item !== region) : [...selected, region])

  return (
    <div className={styles.field}>
      <span>Regions to scan</span>
      <div className={styles.regionGrid}>
        {COMMERCIAL_REGIONS.map((region) => (
          <label key={region} className={styles.check}>
            <input type="checkbox" checked={selected.includes(region)} onChange={() => toggle(region)} />
            <span className={styles.mono}>{region}</span>
          </label>
        ))}
      </div>
    </div>
  )
}
