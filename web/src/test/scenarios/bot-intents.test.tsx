import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import './scenarioSetup'
import StudioChat from '@/components/StudioChat'
import { teamFetch } from '@/api/teamContext'
vi.mock('@/api/teamContext', () => ({ teamFetch: vi.fn(), getActiveTeam: () => null }))
vi.mock('@/api/studioChat', async original => ({
  ...await original<typeof import('@/api/studioChat')>(),
  fetchAvailableProviders: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/components/chat/EmbeddedFileViewer', () => ({ default: () => <div>Report viewer</div> }))
let wire: ReadableStreamDefaultController<Uint8Array>
const frame = (id: string, event: unknown) => `id: ${id}\nevent: transcript\ndata: ${JSON.stringify(event)}\n\n`
const user = { Author: 'user', Content: { parts: [{ text: 'Discuss the plan' }] } }
const reply = { Author: 'assistant', Content: { parts: [{ text: 'We can review it without tools.' }] } }
beforeEach(() => {
  vi.mocked(teamFetch).mockReset().mockImplementation(async () => new Response(new ReadableStream<Uint8Array>({ start(controller) { wire = controller } }), { headers: { 'Content-Type': 'text/event-stream' } }))
})
const emit = async (text: string) => { await act(async () => { wire.enqueue(new TextEncoder().encode(text)) }) }
it('paused conversation uses the shared durable watch, deduplicates replay, and reloads without optimistic copies', async () => {
  const onSend = vi.fn().mockResolvedValue(undefined)
  const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, status: 'paused' as const, toolFree: true, onSend } }
  const view = render(<StudioChat {...props} />)
  await emit('event: replay_complete\ndata: {}\n\n')
  expect(screen.getByText(/Autonomous work paused/)).toBeVisible()
  expect(screen.getByTitle('Attach files (images, PDFs, text files)')).toBeDisabled()
  fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'Discuss the plan' } })
  fireEvent.paste(screen.getByTestId('chat-input'), { clipboardData: { items: [{ kind: 'file', getAsFile: () => new File(['hi'], 'note.txt', { type: 'text/plain' }) }] } })
  expect(screen.getByRole('alert')).toHaveTextContent('files were not attached')
  expect(screen.queryByText('note.txt')).not.toBeInTheDocument()
  expect(screen.getByTestId('chat-input')).toHaveValue('Discuss the plan')
  fireEvent.click(screen.getByTestId('send-button'))
  // No attachments remain undefined; the third argument carries revision-reconciliation operation heads.
  await waitFor(() => expect(onSend).toHaveBeenCalledWith('Discuss the plan', undefined, []))
  expect(screen.queryByText('Discuss the plan')).not.toBeInTheDocument()
  await emit(frame('home:1', user) + frame('home:1', user) + frame('home:2', reply) + frame('home:2', reply))
  expect(screen.getAllByText('Discuss the plan')).toHaveLength(1)
  expect(screen.getAllByText('We can review it without tools.')).toHaveLength(1)
  view.unmount()
  render(<StudioChat {...props} />)
  await emit(frame('home:1', user) + frame('home:2', reply) + 'event: replay_complete\ndata: {}\n\n')
  expect(screen.getAllByText('Discuss the plan')).toHaveLength(1)
  expect(screen.getAllByText('We can review it without tools.')).toHaveLength(1)
  expect(onSend).toHaveBeenCalledTimes(1)
  for (const [url] of vi.mocked(teamFetch).mock.calls) expect(url).toBe('/api/studio/bots/bot/sessions/home/events?scope=personal')
})
it('draft test trace uses the existing read-only transcript and report harness on live and reload', async () => {
  const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'test', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: true, onSend: vi.fn() } }
  const report = { Actions: { StateDelta: { bot_runtime_event: { type: 'artifact', payload: { path: '/report.md', isReport: true } } } } }
  const marker = { Actions: { StateDelta: { bot_runtime_event: { type: 'report_marker', payload: { path: '/report.md', title: 'Test report' } } } } }
  const replay = frame('test:1', user) + frame('test:2', reply) + frame('test:3', report) + frame('test:4', marker) + 'event: replay_complete\ndata: {}\n\n'
  const view = render(<StudioChat {...props} />)
  await emit(replay)
  expect(screen.getByTestId('chat-input')).toBeDisabled()
  expect(await screen.findByTestId('harness-panel')).toHaveAttribute('data-harness-kind', 'report')
  view.unmount()
  render(<StudioChat {...props} />)
  await emit(replay)
  expect(await screen.findByTestId('harness-panel')).toHaveAttribute('data-harness-kind', 'report')
  expect(screen.getAllByText('Discuss the plan')).toHaveLength(1)
  expect(props.botThread.onSend).not.toHaveBeenCalled()
})

it('restores the working indicator before tokens and keeps it across generic done until durable terminal state', async () => {
  const state = { schema_version: 1, bot_id: 'bot', run_id: 'r', session_id: 'trace', home_session_id: 'home', presentation: 'home_interactive', status: 'running' as const, state_sequence: 5 }
  const props = { theme: 'light', hideSidebar: true, initialSessionId: 'home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, runStates: [state], onSend: vi.fn() } }
  const view = render(<StudioChat {...props} />)
  // Bot runs and operations share Working; ordinary chat uses Thinking.
  expect(await screen.findByTestId('thinking-indicator')).toHaveTextContent(/^Working…$/)
  expect(screen.getByTestId('thinking-indicator')).toBeVisible()
  await emit('event: replay_complete\ndata: {}\n\n' + frame('home:3', { Actions: { StateDelta: { bot_runtime_event: { type: 'done', payload: {} } } } }))
  expect(screen.getByTestId('thinking-indicator')).toBeVisible()
  await emit(frame('home:4', { Actions: { StateDelta: { bot_run_state: { ...state, status: 'succeeded', state_sequence: 4 } } } }))
  expect(screen.getByTestId('thinking-indicator')).toBeVisible()
  await emit(frame('home:6', { Actions: { StateDelta: { bot_run_state: { ...state, status: 'waiting_approval', state_sequence: 6 } } } }))
  expect(screen.queryByTestId('thinking-indicator')).not.toBeInTheDocument()
  view.unmount()
  render(<StudioChat {...props} />)
  expect(await screen.findByTestId('thinking-indicator')).toBeVisible()
})

it.each(['stopped', 'deleted'] as const)('quiesces a closed %s Bot despite stale running snapshot', async status => {
  const state = { schema_version: 1, bot_id: 'bot', run_id: 'r', session_id: 'trace', home_session_id: 'home', presentation: 'home_interactive', status: 'running' as const, state_sequence: 5 }
  render(<StudioChat theme="dark" hideSidebar initialSessionId="home" botThread={{botId:'bot',scope:'personal',status,readOnly:true,runStates:[state],onSend:vi.fn()}} />)
  await emit('event: replay_complete\ndata: {}\n\n')
  expect(screen.queryByTestId('thinking-indicator')).not.toBeInTheDocument()
  expect(screen.getByTestId('chat-input')).toBeDisabled()
})
it('transport error cannot claim a running Bot finished', async () => {
  const state = { schema_version: 1, bot_id: 'bot', run_id: 'r', session_id: 'trace', home_session_id: 'home', presentation: 'home_interactive', status: 'running' as const, state_sequence: 5 }
  render(<StudioChat theme="dark" hideSidebar initialSessionId="home" botThread={{botId:'bot',scope:'personal',readOnly:false,runStates:[state],onSend:vi.fn()}} />)
  await emit('event: replay_complete\ndata: {}\n\n')
  await act(async () => wire.error(new Error('Disconnected')))
  expect(screen.getByTestId('thinking-indicator')).toBeVisible()
})
