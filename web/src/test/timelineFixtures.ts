// Fixtures for GET /api/services/{id}/timeline. The contract test checks them against the backend shape.
// Types for these bodies live in api/timeline.ts, so the fixtures stay plain objects.

const NOW = '2026-11-07T12:00:00Z'

function event(fields: {
  id: string
  lane: string
  at: string
  severity: string
  title: string
  actor?: string
  detail?: string
  op?: string
  task_id?: string
  from_revision?: number | null
  to_revision?: number | null
  diff?: { label: string; from: string; to: string }[]
}) {
  return {
    actor: '',
    detail: '',
    op: '',
    task_id: '',
    evidence_at: null,
    from_revision: null,
    to_revision: null,
    diff: [],
    ...fields,
  }
}

const DEPLOY_6 = event({
  id: 'op:4102',
  lane: 'change',
  at: '2026-11-07T11:48:00Z',
  severity: 'change',
  title: 'Deployed revision 6',
  actor: 'sam',
  op: 'deploy',
  from_revision: 5,
  to_revision: 6,
  detail: 'Raise the image resizer batch size.',
})

export const TIMELINE_FAILING = {
  range: '1h',
  from: '2026-11-07T11:00:00Z',
  to: NOW,
  events: [
    DEPLOY_6,
    event({
      id: 'stop:2f9c01aa11bb22cc33dd44ee55ff6601',
      lane: 'stop',
      at: '2026-11-07T11:51:00Z',
      severity: 'bad',
      title: 'Task 2f9c01aa stopped',
      actor: 'ECS',
      detail: 'OutOfMemoryError: Container killed due to memory usage. Exit code 137.',
      task_id: '2f9c01aa11bb22cc33dd44ee55ff6601',
    }),
    event({
      id: 'stop:7b31d2e0a1b2c3d4e5f60718293a4b02',
      lane: 'stop',
      at: '2026-11-07T11:54:00Z',
      severity: 'bad',
      title: 'Task 7b31d2e0 stopped',
      actor: 'ECS',
      detail: 'OutOfMemoryError: Container killed due to memory usage. Exit code 137.',
      task_id: '7b31d2e0a1b2c3d4e5f60718293a4b02',
    }),
    event({
      id: 'stop:c4408f1d99887766554433221100aa03',
      lane: 'stop',
      at: '2026-11-07T11:57:00Z',
      severity: 'bad',
      title: 'Task c4408f1d stopped',
      actor: 'ECS',
      detail: 'OutOfMemoryError: Container killed due to memory usage. Exit code 137.',
      task_id: 'c4408f1d99887766554433221100aa03',
    }),
    event({
      id: 'alert:service_failing',
      lane: 'alert',
      at: '2026-11-07T11:58:00Z',
      severity: 'bad',
      title: 'Alert active: image-resizer is failing',
      actor: 'Harbormaster',
      detail: 'Tasks keep stopping with out of memory errors.',
    }),
    event({
      id: 'terminal:9d1e',
      lane: 'access',
      at: '2026-11-07T11:40:00Z',
      severity: 'info',
      title: 'Terminal session on 2f9c01aa',
      actor: 'omer',
      detail: 'Open for 6 minutes.',
    }),
  ],
  lanes: [
    { lane: 'change', truncated: false, note: '' },
    { lane: 'stop', truncated: false, note: '' },
    { lane: 'alert', truncated: false, note: 'Shows alerts active now. Harbormaster does not keep cleared alerts.' },
    { lane: 'access', truncated: false, note: '' },
  ],
  incident: {
    first_failure_at: '2026-11-07T11:51:00Z',
    last_failure_at: '2026-11-07T11:57:00Z',
    failure_count: 3,
    top_reason: 'Out of memory',
    top_reason_count: 3,
    trigger: DEPLOY_6,
    rollback_revision: 5,
    rolled_back: null,
  },
  deployed_revision: 6,
  polled_at: '2026-11-07T11:59:30Z',
}

export const TIMELINE_EMPTY = {
  range: '1h',
  from: '2026-11-07T11:00:00Z',
  to: NOW,
  events: [],
  lanes: [
    { lane: 'change', truncated: false, note: '' },
    { lane: 'stop', truncated: false, note: '' },
    { lane: 'alert', truncated: false, note: 'Shows alerts active now. Harbormaster does not keep cleared alerts.' },
    { lane: 'access', truncated: false, note: '' },
  ],
  incident: null,
  deployed_revision: 6,
  polled_at: '2026-11-07T11:59:30Z',
}
