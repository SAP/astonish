import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import BotTerminalStatus from '../BotTerminalStatus'
import type { BotRunState } from '@/api/botRunState'
const terminal: BotRunState = { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', home_session_id: 'h', presentation: 'home_interactive', status: 'partial', state_sequence: 2, terminal_reason: 'no_eligible_answer' }
it('renders neutral outcome once outside assistant speech and remains empty for successful or waiting runs', () => {
 const { rerender } = render(<BotTerminalStatus states={{ r: terminal }} />)
 expect(screen.getByRole('status').textContent).toBe('Partially completed — no Employee answer available')
 rerender(<BotTerminalStatus states={{ r: terminal }} />)
 expect(screen.getAllByText('Partially completed — no Employee answer available')).toHaveLength(1)
 for (const status of ['succeeded', 'waiting_approval'] as const) { rerender(<BotTerminalStatus states={{ r: { ...terminal, status } }} />); expect(screen.queryByRole('status')).toBeNull() }
 rerender(<BotTerminalStatus states={{}} />); expect(screen.queryByRole('status')).toBeNull()
})
