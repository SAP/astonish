import { expect, it } from 'vitest'
import { botNeutralTerminalLabel, mergeBotRunState, type BotRunState } from '../botRunState'
const running: BotRunState = { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', home_session_id: 'h', presentation: 'home_interactive', status: 'running', state_sequence: 1 }
it('keeps one neutral terminal outcome across live restore and duplicate reconnect', () => {
 const live = mergeBotRunState({}, running, 'b', 'h')
 const terminal = { ...running, status: 'partial' as const, state_sequence: 2, terminal_reason: 'no_eligible_answer' as const }
 const ended = mergeBotRunState(live, terminal, 'b', 'h')
 expect(botNeutralTerminalLabel(ended.r)).toBe('Partially completed — no Employee answer available')
 expect(mergeBotRunState(ended, terminal, 'b', 'h')).toBe(ended)
 expect(mergeBotRunState({}, terminal, 'b', 'h')).toEqual(ended)
 for (const status of ['running', 'waiting_approval', 'succeeded'] as const) expect(botNeutralTerminalLabel({ ...terminal, status })).toBe('')
 expect(botNeutralTerminalLabel({ ...terminal, terminal_reason: undefined })).toBe('')
})
