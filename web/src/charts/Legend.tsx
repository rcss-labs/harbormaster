import styles from './charts.module.css'

interface LegendProps {
  items: { name: string; color: string }[]
}

/** Colour swatch + name row above a chart (mockup legend()). */
export function Legend({ items }: LegendProps) {
  return (
    <div className={styles.legend} style={{ flexWrap: 'wrap' }}>
      {items.map((item) => (
        <span key={item.name}>
          <i style={{ background: item.color }} />
          {item.name}
        </span>
      ))}
    </div>
  )
}
