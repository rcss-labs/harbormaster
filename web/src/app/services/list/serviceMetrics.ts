import type { ServiceListItemDTO, ServicesMetricsDTO } from '../../../api/types'

export type MetricKind = 'cpu' | 'mem'

export interface MetricFigure {
  /** `41 %`, or a dash when there is no figure: unavailable is never shown as zero. */
  text: string
  /** Why there is a dash; absent when a figure is shown. */
  title?: string
  tone?: 'warn'
}

/** Utilisation above these is drawn in the warning colour. */
const WARN_ABOVE: Record<MetricKind, number> = { cpu: 70, mem: 75 }

export const INSIGHTS_OFF_TITLE = 'Container Insights is off for this cluster'
export const NO_DATA_TITLE = 'No data yet'
export const AWS_ERROR_TITLE = 'CloudWatch returned an error.'
export const THROTTLED_TITLE = 'CloudWatch is throttling requests.'

/**
 * What a row shows for CPU or Memory. `metrics` is the batch response for the page (undefined before the first
 * answer); `failed` means the request itself failed.
 */
export function metricFigure(
  service: Pick<ServiceListItemDTO, 'id' | 'account_id' | 'region'>,
  metrics: ServicesMetricsDTO | undefined,
  failed: boolean,
  kind: MetricKind,
): MetricFigure {
  const item = metrics?.items[service.id]
  if (item?.insights === 'disabled') return { text: '–', title: INSIGHTS_OFF_TITLE }
  const value = item?.[kind]
  if (value !== null && value !== undefined) {
    return { text: `${Math.round(value)} %`, tone: value > WARN_ABOVE[kind] ? 'warn' : undefined }
  }
  const targetError = metrics?.errors.find((error) => error.account_id === service.account_id && error.region === service.region)
  if (targetError) return { text: '–', title: targetError.code === 'throttled' ? THROTTLED_TITLE : AWS_ERROR_TITLE }
  if (failed && !item) return { text: '–', title: AWS_ERROR_TITLE }
  return { text: '–', title: NO_DATA_TITLE }
}
