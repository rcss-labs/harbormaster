import { describe, expect, it } from 'vitest'
import type { ServicesMetricsDTO } from '../../../api/types'
import { metricFigure } from './serviceMetrics'

const service = { id: 's1', account_id: 'a1', region: 'us-east-1' }
const withError = (code: string): ServicesMetricsDTO =>
  ({ items: {}, errors: [{ account_id: 'a1', region: 'us-east-1', code }] }) as unknown as ServicesMetricsDTO

describe('metricFigure', () => {
  it('names a CloudWatch failure in a short word a phone row can carry', () => {
    expect(metricFigure(service, withError('throttled'), false, 'cpu')).toMatchObject({ text: '–', short: 'throttled' })
    expect(metricFigure(service, withError('access_denied'), false, 'cpu')).toMatchObject({ text: '–', short: 'error' })
  })

  it('keeps Insights off and not-yet-measured quiet in the row: the footer explains them', () => {
    const off = { items: { s1: { insights: 'disabled' } }, errors: [] } as unknown as ServicesMetricsDTO
    expect(metricFigure(service, off, false, 'cpu').short).toBeUndefined()
    expect(metricFigure(service, undefined, false, 'cpu').short).toBeUndefined()
  })
})
