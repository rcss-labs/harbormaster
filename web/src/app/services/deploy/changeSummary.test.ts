import { describe, expect, it } from 'vitest'
import type { ChangeSetResponse } from '../../../api/deploy'
import { CHANGESET_APPLIED, CHANGESET_PARTIAL, DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { buildChangeSetRequest, buildRetryRequest, primaryLabel, scalingLines, serviceRows, summarySentence, type ChangeSections } from './changeSummary'

const definition = { family: 'payments-api', cpu: '512', memory: '1024', containerDefinitions: [] }
const none: ChangeSections = { target: null, service: null, scaling: null }

describe('changeSummary: body omits unchanged fields', () => {
  const cases: { name: string; sections: ChangeSections; expected: Record<string, unknown> }[] = [
    { name: 'definition only', sections: { ...none, target: { kind: 'definition', definition } }, expected: { task_definition: { definition } } },
    { name: 'revision only', sections: { ...none, target: { kind: 'revision', revision: 41 } }, expected: { task_definition: { revision: 41 } } },
    { name: 'service only', sections: { ...none, service: { desired_count: 5 } }, expected: { service: { desired_count: 5 } } },
    { name: 'scaling only', sections: { ...none, scaling: { delete_policies: ['a'] } }, expected: { scaling: { delete_policies: ['a'] } } },
    {
      name: 'everything',
      sections: { target: { kind: 'revision', revision: 41 }, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } } },
      expected: { task_definition: { revision: 41 }, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } } },
    },
  ]
  it.each(cases)('$name', ({ sections, expected }) => {
    const body = buildChangeSetRequest({ baseRevision: 42, baseVersion: 'v1', reason: '  why  ', sections })
    expect(Object.keys(body).sort()).toEqual(['base_revision', 'base_version', 'reason', ...Object.keys(expected)].sort())
    expect(body).toMatchObject({ base_revision: 42, reason: 'why', ...expected })
  })

  it('sends no base_version for a definition-only change set', () => {
    const body = buildChangeSetRequest({ baseRevision: 42, baseVersion: 'v1', reason: '', sections: { ...none, target: { kind: 'revision', revision: 41 } } })
    expect(body.base_version).toBe('')
  })
})

describe('changeSummary: scan gate acknowledgement', () => {
  const scanGate = { deploy_anyway: true, images: [{ container: 'app', ref: 'sha256:abc' }] }
  const withGate = {
    ...buildChangeSetRequest({ baseRevision: 42, baseVersion: 'v1', reason: '', sections: { ...none, target: { kind: 'definition', definition } } }),
    scan_gate: scanGate,
  }
  const failedRegister: ChangeSetResponse = {
    ...CHANGESET_PARTIAL,
    registered_revision: null,
    steps: [
      { name: 'register', status: 'failed', message: 'x' },
      { name: 'service', status: 'skipped', message: '' },
      { name: 'scaling', status: 'skipped', message: '' },
    ],
  }

  it('a retry that sends the definition again keeps the acknowledgement', () => {
    expect(buildRetryRequest(withGate, failedRegister)?.scan_gate).toEqual(scanGate)
  })

  it('a retry by registered revision is not gated and drops it', () => {
    const retry = buildRetryRequest(withGate, { ...CHANGESET_PARTIAL, registered_revision: 43, steps: [
      { name: 'register', status: 'done', message: '' },
      { name: 'service', status: 'failed', message: 'x' },
      { name: 'scaling', status: 'skipped', message: '' },
    ] })
    expect(retry?.task_definition).toEqual({ revision: 43 })
    expect(retry).not.toHaveProperty('scan_gate')
  })
})

describe('buildRetryRequest', () => {
  const original = buildChangeSetRequest({
    baseRevision: 42,
    baseVersion: 'v1',
    reason: 'r',
    sections: { target: { kind: 'definition', definition }, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } } },
  })

  it('keeps failed and skipped sections, swaps in the registered revision and the fresh version', () => {
    const retry = buildRetryRequest(original, { ...CHANGESET_PARTIAL, steps: [
      { name: 'register', status: 'done', message: '' },
      { name: 'service', status: 'failed', message: 'x' },
      { name: 'scaling', status: 'skipped', message: '' },
    ] })
    expect(retry).toMatchObject({ base_version: CHANGESET_PARTIAL.settings_version, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } }, reason: 'r', task_definition: { revision: CHANGESET_PARTIAL.registered_revision } })
  })

  it('drops sections that are done', () => {
    const response: ChangeSetResponse = {
      ...CHANGESET_PARTIAL,
      steps: [
        { name: 'register', status: 'done', message: '' },
        { name: 'service', status: 'done', message: '' },
        { name: 'scaling', status: 'failed', message: 'x' },
      ],
    }
    const retry = buildRetryRequest(original, response)
    expect(retry?.service).toBeUndefined()
    expect(retry?.scaling).toEqual({ target: { min: 1, max: 4 } })
  })

  it('sends the original definition again when register itself failed', () => {
    const response: ChangeSetResponse = {
      ...CHANGESET_PARTIAL,
      registered_revision: null,
      steps: [
        { name: 'register', status: 'failed', message: 'x' },
        { name: 'service', status: 'skipped', message: '' },
        { name: 'scaling', status: 'skipped', message: '' },
      ],
    }
    expect(buildRetryRequest(original, response)?.task_definition).toEqual({ definition })
  })

  it('re-sends only the registered revision when register is done and the service step failed', () => {
    const retry = buildRetryRequest(original, { ...CHANGESET_PARTIAL, registered_revision: 43, to_revision: null, steps: [
      { name: 'register', status: 'done', message: '' },
      { name: 'service', status: 'failed', message: 'x' },
      { name: 'scaling', status: 'skipped', message: '' },
    ] })
    expect(retry).toMatchObject({ base_revision: 42, task_definition: { revision: 43 }, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } } })
  })

  it('drops the definition and rebases onto the deployed revision when register and service are done', () => {
    const retry = buildRetryRequest(original, { ...CHANGESET_PARTIAL, registered_revision: 43, to_revision: 43, steps: [
      { name: 'register', status: 'done', message: '' },
      { name: 'service', status: 'done', message: '' },
      { name: 'scaling', status: 'failed', message: 'x' },
    ] })
    expect(retry?.task_definition).toBeUndefined()
    expect(retry?.base_revision).toBe(43)
    expect(retry?.scaling).toEqual({ target: { min: 1, max: 4 } })
  })

  it('has nothing to retry when every step is done', () => {
    expect(buildRetryRequest(original, CHANGESET_APPLIED)).toBeNull()
  })
})

describe('describing a change', () => {
  it('lists service rows as before and after', () => {
    expect(serviceRows(DEPLOY_SETTINGS_PAYMENTS, { desired_count: 5, enable_execute_command: false, force_new_deployment: true })).toEqual([
      { label: 'Desired count', before: '3', after: '5' },
      { label: 'ECS Exec', before: 'on', after: 'off' },
      { label: 'Restart tasks', before: 'no', after: 'yes' },
    ])
  })

  it('lists scaling changes', () => {
    const lines = scalingLines(DEPLOY_SETTINGS_PAYMENTS, { target: { min: 1, max: 10 }, delete_policies: ['cpu-target-60'], delete_scheduled_actions: ['business-hours'] })
    expect(lines).toEqual(['Capacity 2–8 → 1–10', 'Remove policy cpu-target-60', 'Remove scheduled action business-hours'])
  })

  const labelCases: [string, ChangeSections, boolean, string][] = [
    ['definition + deploy', { ...none, target: { kind: 'definition', definition } }, true, 'Register and deploy :44'],
    ['revision + deploy', { ...none, target: { kind: 'revision', revision: 41 } }, true, 'Deploy :41'],
    ['no definition', { ...none, service: { desired_count: 5 } }, true, 'Apply changes'],
    ['edit only, nothing else', { ...none, target: { kind: 'definition', definition } }, false, 'Register :44'],
    ['edit only plus service', { ...none, target: { kind: 'definition', definition }, service: { desired_count: 5 } }, false, 'Apply changes'],
  ]
  it.each(labelCases)('button label: %s', (_name, sections, canDeploy, label) => {
    expect(primaryLabel({ sections, predictedRevision: 44, canDeploy })).toBe(label)
  })

  it('summarises in a sentence', () => {
    const sentence = summarySentence({
      serviceName: 'payments-api',
      sections: { target: { kind: 'definition', definition }, service: { desired_count: 5 }, scaling: { target: { min: 1, max: 4 } } },
      predictedRevision: 44,
      canDeploy: true,
    })
    expect(sentence).toBe('Registers revision :44 and deploys it to payments-api, changes the service settings and changes auto scaling.')
  })
})
