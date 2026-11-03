import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { SecretsEditor } from './SecretsEditor'
import type { SecretEntry } from './draft'

const arn = 'arn:aws:secretsmanager:us-east-1:482109375512:secret:prod/db-AbCdEf'

function Harness({ initial, isAdmin = true }: { initial: SecretEntry[]; isAdmin?: boolean }) {
  const [entries, setEntries] = useState(initial)
  return <SecretsEditor entries={entries} onChange={setEntries} isAdmin={isAdmin} />
}

describe('SecretsEditor', () => {
  it('never renders a value and rejects a bad valueFrom', async () => {
    const user = userEvent.setup()
    // A stray `value` must not reach the screen, whatever the caller passes.
    const leaky = { name: 'DB_PASSWORD', valueFrom: arn, value: 'hunter2' } as SecretEntry
    render(<Harness initial={[leaky]} />)

    expect(screen.getByDisplayValue('DB_PASSWORD')).toBeInTheDocument()
    expect(screen.getByDisplayValue(arn)).toBeInTheDocument()
    expect(screen.queryByText(/hunter2/)).not.toBeInTheDocument()
    expect(screen.queryByDisplayValue('hunter2')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    const reference = screen.getByRole('textbox', { name: 'Value from' })
    await user.clear(reference)
    await user.type(reference, 'not a secret')
    expect(screen.getByRole('alert')).toHaveTextContent(/Secrets Manager ARN, an SSM parameter ARN or an SSM parameter name/)

    await user.clear(reference)
    await user.type(reference, '/prod/db/password')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    await user.clear(reference)
    await user.type(reference, 'arn:aws:ssm:eu-west-1:482109375512:parameter/prod/api/key')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('always notes the execution-role permissions; non-admins also get the prefix rule', () => {
    const { unmount } = render(<Harness initial={[]} isAdmin />)
    const note = screen.getByText(/execution role/i)
    expect(note).toHaveTextContent('secretsmanager:GetSecretValue')
    expect(note).toHaveTextContent('ssm:GetParameters')
    expect(note).toHaveTextContent('kms:Decrypt')
    expect(screen.queryByText(/global admin/i)).not.toBeInTheDocument()
    unmount()

    render(<Harness initial={[]} isAdmin={false} />)
    expect(screen.getByText(/global admin/i)).toHaveTextContent(/prefix/i)
  })
})
