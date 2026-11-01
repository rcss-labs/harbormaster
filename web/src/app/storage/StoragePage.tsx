import { type StorageItem, useStorage } from '../../api/storage'
import { HBars } from '../../charts/HBars'
import { Donut } from '../../charts/Donut'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { SERIES } from '../../components/status'
import { useCrumbs } from '../shell/crumbs'
import { EfsCard } from './EfsCard'
import { formatSize } from './format'
import styles from './storage.module.css'

export function StoragePage() {
  useCrumbs([['Storage']])
  const storage = useStorage()
  const response = storage.data
  const items = response?.items ?? []

  if (storage.isError) {
    return (
      <>
        <PageHead />
        <EmptyState>{storage.error instanceof Error ? storage.error.message : 'The storage could not be loaded.'}</EmptyState>
      </>
    )
  }
  if (!response) return <PageHead />

  const mountedCount = items.filter((fs) => fs.mounted).length
  const barColour = (fs: StorageItem, index: number) => (fs.mounted ? SERIES[index % SERIES.length] : 'var(--border-strong)')

  return (
    <>
      <PageHead />
      {items.length === 0 ? (
        <EmptyState>No file systems you can see.</EmptyState>
      ) : (
        <>
          <div className={styles.gridWide}>
            <Card title="Stored data by file system" right={`${formatSize(response.totals.size_bytes)} in total`}>
              <HBars
                items={items.map((fs, index) => ({
                  name: fs.name,
                  sub: fs.region,
                  v: fs.size_bytes,
                  color: barColour(fs, index),
                  label: formatSize(fs.size_bytes),
                }))}
              />
            </Card>
            <Card title="Mounted or not">
              <Donut
                segments={[
                  { name: 'Mounted by a service', v: mountedCount, color: 'var(--accent)' },
                  { name: 'Not mounted, still billed', v: items.length - mountedCount, color: 'var(--warning)' },
                ]}
                label={String(items.length)}
                sub="file systems"
              />
            </Card>
          </div>
          <div className={styles.cards}>
            {items.map((fs, index) => (
              <EfsCard key={fs.file_system_id} fs={fs} colour={barColour(fs, index)} />
            ))}
          </div>
        </>
      )}
    </>
  )
}

function PageHead() {
  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <h1>Storage</h1>
        <div className={styles.meta}>EFS file systems and the services that mount them. File systems are created by Terraform.</div>
      </div>
    </div>
  )
}
