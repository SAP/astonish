import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotOperationResult from '../BotOperationResult'
import { decideBotOperation } from '@/api/botReviews'
import type { BotOperationView } from '@/api/botOperationResult'
vi.mock('@/api/botReviews', () => ({ decideBotOperation: vi.fn() }))
const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'blocked', decision: 'require_authorization', summary: 'Daily Boston weather for you', exception: { fields: [{ caption: 'Instructions', value: 'Daily Boston weather for you' }], operation_id: 'one', payload_digest: 'secret-digest', expires_at: '2099-01-01T00:00:00Z' } }
it.each(['pending', 'applying', 'applied', 'blocked', 'failed', 'cancelled'] as const)('renders %s with no routine review buttons or raw digest', status => {
  render(<BotOperationResult operation={{ ...operation, status, decision: 'execute' }} scope="personal" readOnly={false} onChange={vi.fn()} />)
  expect(screen.getByRole('heading')).toHaveTextContent(status.charAt(0).toUpperCase() + status.slice(1))
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  expect(screen.queryByText(/secret-digest/)).not.toBeInTheDocument()
})
it('makes team readers read-only and hides unknown-scope approvals', () => {
  const { rerender } = render(<BotOperationResult operation={operation} scope="team" readOnly onChange={vi.fn()} />)
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  rerender(<BotOperationResult operation={{ ...operation, operation: 'unknown' }} scope="team" readOnly={false} onChange={vi.fn()} />)
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})
it.each([true, false])('waits for authoritative decision %s and does not claim applied', async approve => {
  const changed = vi.fn()
  vi.mocked(decideBotOperation).mockResolvedValue({ ...operation, status: approve ? 'pending' : 'cancelled', revision: 2, state_sequence: 2 })
  render(<BotOperationResult operation={operation} scope="personal" readOnly={false} onChange={changed} />)
  fireEvent.click(screen.getByRole('button', { name: approve ? 'Approve create schedule' : 'Deny' }))
  await waitFor(() => expect(changed).toHaveBeenCalledWith(expect.objectContaining({ status: approve ? 'pending' : 'cancelled' })))
  expect(screen.queryByText(/Schedule enabled/)).not.toBeInTheDocument()
})
