import { expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import BotOperationResult from '../BotOperationResult'
import type { BotOperationView } from '@/api/botOperationResult'

const operation: BotOperationView = {
  schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'forget-one',
  revision: 1, state_sequence: 1, operation: 'memory_forget', status: 'blocked',
  decision: 'require_authorization', summary: 'Change needs attention.',
  exception: { operation_id: 'forget-one', payload_digest: 'private-digest', expires_at: '2099-01-01T00:00:00Z', fields: [
    { caption: 'Change', value: 'memory_forget' },
    { caption: 'Memory', value: '0714d1d8-d03a-54f4-9153-1b6799e3a8cb' },
    { caption: 'Recipient', value: 'owner@example.test' },
    { caption: 'Cost limit', value: 'USD 0.25' },
  ] },
}

it.each([false, true])('shows exact bounded fields and expiry for owner/read-only=%s', readOnly => {
  render(<BotOperationResult operation={operation} scope="personal" readOnly={readOnly} onChange={vi.fn()} />)
  const card = screen.getByRole('article', { name: 'Operation result' })
  expect(card).toHaveAttribute('data-operation-id', operation.operation_id)
  expect(within(card).getByRole('heading')).toHaveTextContent('Forget information · Blocked')
  for (const field of operation.exception!.fields!.filter(f => f.caption !== 'Change')) {
    expect(within(card).getByText(field.caption)).toBeVisible()
    expect(within(card).getByText(field.value)).toBeVisible()
  }
  expect(within(card).getByText('Permission requested for this saved action only.')).toBeVisible()
  expect(within(card).getByText('Expires')).toBeVisible()
  expect(within(card).getByText('2099-01-01T00:00:00Z')).toBeVisible()
  expect(card).not.toHaveTextContent('private-digest')
  expect(card).not.toHaveTextContent('memory_forget')
  if (readOnly) expect(within(card).queryByRole('button')).not.toBeInTheDocument()
})

it('keeps the exact expired lifetime visible without decision controls', () => {
  render(<BotOperationResult operation={{ ...operation, exception: { ...operation.exception!, expires_at: '2000-01-01T00:00:00Z' } }} scope="personal" readOnly={false} onChange={vi.fn()} />)
  expect(screen.getByText('2000-01-01T00:00:00Z')).toBeVisible()
  expect(screen.getByText('This request expired. Ask for a fresh action.')).toBeVisible()
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})
