import { describe, expect, it } from 'vitest'
import { CAPACITY_PARAMS, parseCapacityParams, spotHref, spotReason } from './capacityParams'

const parse = (query: string) => parseCapacityParams(new URLSearchParams(query))

describe('capacityParams', () => {
  it('parses and rejects capacity parameters', () => {
    expect(parse('capacity=spot&on_demand_base=1')).toEqual({ onDemandBase: 1 })
    expect(parse('capacity=spot&on_demand_base=0')).toEqual({ onDemandBase: 0 })
    expect(parse('capacity=spot&on_demand_base=1000')).toEqual({ onDemandBase: 1000 })

    expect(parse('capacity=spot&on_demand_base=1.5')).toBeNull()
    expect(parse('capacity=spot&on_demand_base=abc')).toBeNull()
    expect(parse('capacity=spot&on_demand_base=-1')).toBeNull()
    expect(parse('capacity=spot&on_demand_base=1001')).toBeNull()
    expect(parse('capacity=spot&on_demand_base=')).toBeNull()
    expect(parse('capacity=spot')).toBeNull()
    expect(parse('on_demand_base=1')).toBeNull()
    expect(parse('capacity=on_demand&on_demand_base=1')).toBeNull()
    expect(parse('capacity=SPOT&on_demand_base=1')).toBeNull()
  })

  it('builds the link a Cost row opens and names the parameters to clear', () => {
    expect(spotHref('svc/1', 1)).toBe('/services/svc%2F1/deploy?capacity=spot&on_demand_base=1')
    expect(CAPACITY_PARAMS).toEqual(['capacity', 'on_demand_base'])
  })

  it('words the reason', () => {
    expect(spotReason(1)).toBe('Move to Fargate Spot, 1 on-demand')
  })
})
