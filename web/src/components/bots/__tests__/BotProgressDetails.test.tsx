import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import BotProgressDetails from '../BotProgressDetails'
import { BotPolicySummary } from '../BotControls'
import type { BotRunState } from '@/api/botRunState'
import type { BotVersion } from '@/api/bots'
const state: BotRunState = { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', home_session_id: 'h', presentation: '', status: 'running', state_sequence: 1, progress: { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', revision: 3, phase: 'synthesizing', active_duration_nanos: 21600000000000, summary: '<script>data only</script>', evaluated_at: '2026-01-01T00:00:00Z' } }
it('shows checkpoint details as text, not a final result, with no invented count', () => {
  const view = render(<BotProgressDetails state={state} />)
  expect(screen.getByText('Preparing result')).toBeInTheDocument()
  expect(screen.getByText('<script>data only</script>')).toBeInTheDocument()
  expect(view.container.querySelector('script')).toBeNull()
  expect(screen.getByText('Cumulative active time: 21600 seconds')).toBeInTheDocument()
  expect(view.container).not.toHaveTextContent(/coalesced/)
  view.rerender(<BotProgressDetails state={{ ...state, progress: { ...state.progress!, bot_id: 'foreign' } }} />)
  expect(view.container).toBeEmptyDOMElement()
})
it.each([0, 7])('shows only the recorded coalesced occurrence count %s', coalesced_occurrences => {
  const view = render(<BotProgressDetails state={{ ...state, state_sequence: 999, progress: { ...state.progress!, coalesced_occurrences } }} />)
  expect(view.container).toHaveTextContent(`Coalesced schedule ticks: ${coalesced_occurrences}`)
  expect(view.container).not.toHaveTextContent('999')
})
it('shows progress owner duration instead of the ignored saved legacy seconds', () => {
  const version = { persona: { timezone: 'UTC' }, autonomy: { level: 'ask', max_spend_per_run_minor: 0 }, delivery: { notify: 'never' }, budget: { max_runtime_seconds: 900, runtime_policy: { schema_version: 1, mode: 'progress_v1', evaluation_interval_seconds: 300, model_call_timeout_seconds: 120, tool_call_timeout_seconds: 120 } } } as BotVersion
  const view = render(<BotPolicySummary version={version} />)
  expect(view.container).toHaveTextContent('No task duration limit; token, tool and spending limits still apply.')
  expect(view.container).not.toHaveTextContent('900s/run')
})
