import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import BotOperationResult from '../BotOperationResult'
import { OperationPresentation, type BotOperationView } from '@/api/botOperationResult'

const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending', decision: 'execute', summary: 'Change queued.', presentation: OperationPresentation.Internal }
const props = { scope: 'personal' as const, readOnly: false, onChange: vi.fn() }

it.each(['pending', 'applying'] as const)('does not mount a card or bubble for internal %s', status => {
  const { container } = render(<BotOperationResult {...props} operation={{ ...operation, status }} />)
  expect(container).toBeEmptyDOMElement()
})
it.each(['applied', 'failed', 'blocked', 'cancelled'] as const)('leaves conversation %s rendering to the ordinary agent path', status => {
  const { container } = render(<BotOperationResult {...props} operation={{ ...operation, status, presentation: OperationPresentation.Conversation, summary: 'Confirmed outcome', blocker: 'Explanation' }} />)
  expect(container).toBeEmptyDOMElement()
})
it.each([undefined, ...Object.values(OperationPresentation)])('preserves authorization actions even if presentation is %s', presentation => {
  render(<BotOperationResult {...props} operation={{ ...operation, presentation, status: 'blocked', decision: 'require_authorization', exception: { operation_id: 'one', payload_digest: 'digest', expires_at: '2099-01-01T00:00:00Z', fields: [{ caption: 'Instructions', value: 'Daily weather' }] } }} />)
  expect(screen.getByRole('article', { name: 'Operation result' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Approve create schedule' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Deny' })).toBeEnabled()
})
