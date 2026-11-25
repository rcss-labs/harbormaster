import type { AgentUsageDTO } from '../../../api/agent'
import { Panel } from '../../../components/Panel'
import { Table, tableClass } from '../../../components/Table'

const count = new Intl.NumberFormat('en-US')

const ROLE_LABELS: Record<string, string> = { reviewer: 'Reviews' }

/** Runs and cost per day and role for the current month. */
export function UsageTable({ usage, loading, error }: { usage?: AgentUsageDTO; loading: boolean; error: boolean }) {
  return (
    <Panel title="Usage" sub={usage?.month} loading={loading} error={error ? { message: 'Could not load the usage.' } : undefined}>
      {usage && usage.days.length === 0 ? (
        <p>Nothing has been used this month.</p>
      ) : (
        <Table>
          <thead>
            <tr>
              <th>Day</th>
              <th>Role</th>
              <th className={tableClass.num}>Runs</th>
              <th className={tableClass.num}>Input tokens</th>
              <th className={tableClass.num}>Output tokens</th>
              <th className={tableClass.num}>Cost</th>
            </tr>
          </thead>
          <tbody>
            {usage?.days.map((row) => (
              <tr key={`${row.day}/${row.role}`}>
                <td>{row.day}</td>
                <td>{ROLE_LABELS[row.role] ?? row.role}</td>
                <td className={tableClass.num}>{count.format(row.runs)}</td>
                <td className={tableClass.num}>{count.format(row.input_tokens)}</td>
                <td className={tableClass.num}>{count.format(row.output_tokens)}</td>
                <td className={tableClass.num}>${row.cost_usd}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  )
}
