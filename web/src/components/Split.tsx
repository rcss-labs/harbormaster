import styles from './Split.module.css'

export function Split({ parts }: { parts: { n: number; color: string }[] }) {
  return (
    <div className={styles.split}>
      {parts
        .filter((part) => part.n > 0)
        .map((part, index) => (
          <span key={index} style={{ flex: part.n, background: part.color }} title={String(part.n)} />
        ))}
    </div>
  )
}
