import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import BotOperationResult from '../BotOperationResult'
import { decideBotOperation } from '@/api/botReviews'
import type { BotOperationView } from '@/api/botOperationResult'
vi.mock('@/api/botReviews', () => ({ decideBotOperation: vi.fn() }))
const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'blocked', decision: 'require_authorization', summary: 'HTTP secret', blocker: 'stack secret', exception: { fields: [{ caption: 'Instructions', value: 'Send daily weather' }], operation_id: 'one', payload_digest: 'exact-digest', expires_at: '2099-01-01T00:00:00Z' } }
it.each([401, 403, 409, 500])('keeps the exact card and handles decision failure %s safely', async status => {
  vi.mocked(decideBotOperation).mockRejectedValue({ status, message: 'secret HTTP body' })
  const changed = vi.fn()
  render(<BotOperationResult operation={operation} scope="personal" readOnly={false} onChange={changed} />)
  expect(screen.getByText('Send daily weather')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Approve create schedule' }))
  const alert = await screen.findByRole('alert')
  expect(alert).not.toHaveTextContent(/secret|HTTP|stack/)
  expect(screen.queryByText(/secret/)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
  expect(changed).not.toHaveBeenCalled()
  expect(decideBotOperation).toHaveBeenLastCalledWith({ id: 'bot', scope: 'personal' }, operation, true)
})
it.each([true, false])('preserves read-only and expired authority after reload (%s)', readOnly => {
  const view = render(<BotOperationResult operation={operation} scope="team" readOnly={readOnly} onChange={vi.fn()} />)
  if (readOnly) expect(screen.queryByRole('button')).not.toBeInTheDocument()
  view.rerender(<BotOperationResult operation={{ ...operation, exception: { ...operation.exception!, expires_at: '2000-01-01T00:00:00Z' } }} scope="team" readOnly={readOnly} onChange={vi.fn()} />)
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  expect(screen.getByText('Send daily weather')).toBeVisible()
})
