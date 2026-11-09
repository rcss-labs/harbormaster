import type { LogSearchResultDTO } from '../../api/logSearch'
import { HBars } from '../../charts/HBars'
import { hitsByService } from './model'

export function HitsByService({ result }: { result: LogSearchResultDTO }) {
  const items = hitsByService(result.rows, result.services).map((service) => ({ name: service.name, v: service.count }))
  return <HBars items={items} />
}
