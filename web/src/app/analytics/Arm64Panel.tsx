import type { Arm64Service, CostArm64 } from '../../api/analytics'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { Pill } from '../../components/Pill'
import { blockerText } from '../services/deploy/Arm64Notes'
import { AnalyticsPanel } from './AnalyticsPanel'
import { ServiceLink } from './cells'
import { arm64Rows, isArm64Ticked, money, selectedArm64 } from './cost'
import styles from './analytics.module.css'

interface Arm64PanelProps {
  arm64: CostArm64
  /** What the user explicitly ticked or unticked, by service id; rows not in it follow the default. */
  choices: ReadonlyMap<string, boolean>
  onChoose: (serviceId: string, ticked: boolean) => void
  onDraft: () => void
}

const savingText = (service: Arm64Service) => `${service.spot_tasks > 0 ? 'about ' : ''}${money(service.saving_monthly ?? 0)}`

/** x86 Fargate services that could move to ARM64, with the images that keep a service from being ready. */
export function Arm64Panel({ arm64, choices, onChoose, onDraft }: Arm64PanelProps) {
  const sub = 'x86 Fargate services by monthly saving at list price'
  if (!arm64.check_enabled) {
    return (
      <AnalyticsPanel title="ARM64 (Graviton)" sub={sub} showSources={false}>
        <EmptyState>The image check is off, so there are no ARM64 suggestions. Set HM_ARM64_CHECK=true to turn it on.</EmptyState>
      </AnalyticsPanel>
    )
  }
  const rows = arm64Rows(arm64)
  const selected = selectedArm64(arm64, choices)
  const saving = selected.reduce((total, service) => total + (service.saving_monthly ?? 0), 0)
  const includesSpot = selected.some((service) => service.spot_tasks > 0)

  const columns: DataTableColumn<Arm64Service>[] = [
    {
      key: 'select',
      header: <span className="hm-sr">Select</span>,
      width: '36px',
      phone: 'action',
      render: (service) => (
        <input
          type="checkbox"
          aria-label={`Select ${service.service}`}
          checked={isArm64Ticked(service, choices)}
          disabled={service.status !== 'ready' || !service.can_edit}
          title={service.can_edit ? undefined : `You need Edit on ${service.tag.name}`}
          onChange={() => onChoose(service.service_id, !isArm64Ticked(service, choices))}
        />
      ),
    },
    {
      key: 'service',
      header: 'Service',
      width: 'minmax(180px, 1.4fr)',
      sortValue: (service) => service.service,
      render: (service) => (
        <>
          <ServiceLink id={service.service_id} name={service.service} cluster={service.cluster} />
          {service.auto_scaled && (
            <span className={styles.pills}>
              <Pill kind="info" title="A different task size may change how many tasks run, so the saving is an estimate.">
                Auto-scaled
              </Pill>
            </span>
          )}
        </>
      ),
    },
    {
      key: 'tasks',
      header: 'Tasks',
      width: '90px',
      render: (service) => <span className={styles.mono}>{`${service.tasks} ${service.tasks === 1 ? 'task' : 'tasks'}`}</span>,
    },
    {
      key: 'images',
      header: 'Images',
      width: 'minmax(260px, 2fr)',
      render: (service) => {
        const withBuild = service.images.filter((image) => image.state === 'arm64').length
        return (
          <>
            <span>{`${withBuild} of ${service.images.length} images have an arm64 build`}</span>
            <ul className={styles.whyList}>
              {service.images
                .filter((image) => image.state !== 'arm64')
                .map((image) => (
                  <li key={`${image.container}-${image.reason}`}>
                    <Pill kind={image.state === 'no_arm64' ? 'warn' : 'mute'}>{image.state === 'no_arm64' ? 'Blocked' : 'Not checked'}</Pill> {blockerText(image)}
                  </li>
                ))}
            </ul>
          </>
        )
      },
    },
    {
      key: 'saves',
      header: 'Saves / mo',
      width: '110px',
      align: 'right',
      sortValue: (service) => service.saving_monthly,
      render: (service) => <span className={styles.mono}>{savingText(service)}</span>,
    },
  ]

  return (
    <AnalyticsPanel title="ARM64 (Graviton)" sub={sub} showSources={false} flush>
      {rows.length === 0 ? (
        <EmptyState>Every Fargate service already runs on ARM64, or none runs on x86 Fargate.</EmptyState>
      ) : (
        <>
          <DataTable label="ARM64 suggestions" columns={columns} rows={rows} rowKey={(service) => service.service_id} minWidth={900} phoneSort />
          <div className={styles.more}>
            <span>{`${selected.length} selected · ${includesSpot ? 'about ' : ''}${money(saving)} per month`}</span>
            <button type="button" disabled={selected.length === 0} onClick={onDraft}>
              Draft change sets
            </button>
          </div>
          <p className={styles.kpiNote}>Savings are per advisor and do not add up: each is measured against how the service runs today.</p>
        </>
      )}
    </AnalyticsPanel>
  )
}
