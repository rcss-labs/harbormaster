import styles from './ChartLegend.module.css'

export interface ChartLegendItem {
  label: string
  color: string
  /** Mono figure after the label, e.g. a total or an average. */
  value?: string
}

/** Panel header legend: a 3px colour bar, the series name and a mono value. */
export function ChartLegend({ items }: { items: ChartLegendItem[] }) {
  return (
    <ul className={styles.legend}>
      {items.map((item) => (
        <li key={item.label}>
          <i style={{ background: item.color }} aria-hidden="true" />
          <span>{item.label}</span>
          {item.value !== undefined && <b>{item.value}</b>}
        </li>
      ))}
    </ul>
  )
}
