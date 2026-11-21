import { describe, expect, it } from 'vitest'
import { CONFIG_SEARCH_NODE } from '../../test/configSearchFixtures'
import { appendField, csvFilename, queryFromParams, resultCountLabel, matchLabel, selectionOf, toCSV } from './model'

describe('config search model', () => {
  it('reads the query from the URL', () => {
    expect(queryFromParams(new URLSearchParams('q=image%3Anode%3A18'))).toBe('image:node:18')
    expect(queryFromParams(new URLSearchParams(''))).toBe('')
  })

  it('appends a field once', () => {
    expect(appendField('', 'env')).toBe('env:')
    expect(appendField('image:node:18', 'env')).toBe('image:node:18 env:')
    expect(appendField('image:node:18 ', 'port')).toBe('image:node:18 port:')
    expect(appendField('env:', 'env')).toBe('env:')
  })

  it('labels one and many results', () => {
    expect(resultCountLabel(1)).toBe('1 service matches')
    expect(resultCountLabel(0)).toBe('0 services match')
    expect(resultCountLabel(3)).toBe('3 services match')
  })

  it('labels a match with its container when it is not the first', () => {
    expect(matchLabel({ field: 'env', container: 'app', value: 'DATABASE_URL' }, 'app')).toBe('env DATABASE_URL')
    expect(matchLabel({ field: 'port', container: 'api', value: '8080' }, 'web')).toBe('port 8080 (api)')
    expect(matchLabel({ field: 'taskrole', container: '', value: 'arn:x' }, 'web')).toBe('taskrole arn:x')
  })

  it('writes one CSV line per container with the header', () => {
    const csv = toCSV(CONFIG_SEARCH_NODE.items)
    const lines = csv.split('\r\n')
    expect(lines[0]).toBe(
      'service,cluster,account_id,account_name,region,permission_tag,family,revision,matched_revision,previous_revision,container,image,image_tag,base_image,ports,env_names,secret_names,task_role,execution_role,log_group,matched,service_id',
    )
    // api (1 container) + notifications (1) + payments-api (2), then the trailing line end
    expect(lines).toHaveLength(1 + 4 + 1)
    expect(lines.at(-1)).toBe('')
    const payments = lines.filter((line) => line.startsWith('payments-api,'))
    expect(payments).toHaveLength(2)
    expect(payments[0]).toContain(',node:18-alpine,8080,NODE_ENV PAYMENTS_PROVIDER DB_POOL_SIZE,')
    expect(lines.find((line) => line.startsWith('notifications,'))).toContain(',10,9,yes,')
  })

  it('guards cells that start with = + - @ and quotes commas', () => {
    const csv = toCSV(CONFIG_SEARCH_NODE.items)
    // the planted environment name =cmd and role =cmd|role are written with a leading apostrophe
    expect(csv).toContain("NODE_ENV '=cmd")
    expect(csv).toContain(",'=cmd|role,")
    expect(csv).not.toMatch(/(^|,)=cmd/)
    const quoted = toCSV([{ ...CONFIG_SEARCH_NODE.items[0], name: 'a,"b"\nc', containers: [] }])
    expect(quoted).toContain('"a,""b""\nc"')
    for (const first of ['+', '-', '@']) {
      const guarded = toCSV([{ ...CONFIG_SEARCH_NODE.items[0], family: `${first}x`, containers: [] }])
      expect(guarded).toContain(`'${first}x`)
    }
  })

  it('names the file with the local date and time', () => {
    expect(csvFilename(new Date(2026, 10, 21, 9, 5))).toBe('harbormaster-config-search-20261121-0905.csv')
  })

  it('builds a selection from the query, filters and ids', () => {
    expect(selectionOf('env:PORT', { account: '1', tag: [3] }, ['a', 'b'])).toEqual({
      query: 'env:PORT',
      filters: { account: '1', tag: [3] },
      serviceIds: ['a', 'b'],
    })
  })
})
