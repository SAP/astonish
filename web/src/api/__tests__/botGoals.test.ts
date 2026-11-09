import { beforeEach, expect, it, vi } from 'vitest'
import { teamFetch } from '../teamContext'
import { BotAPIError, type Bot } from '../bots'
import { createGoal, editGoal, confirmGoal, listTrackedItems, saveTrackedItem, TrackedStatus } from '../botGoals'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const bot = { id: 'b/1', scope: 'team' } as Bot
const fields = { title: 'Outcome', description: 'Details', completion_criteria: 'Verified', due_at: null }
const proof = { summary: 'Checked', source: 'Report', observed_at: '2020-01-01T10:00:00Z' }
beforeEach(() => vi.resetAllMocks())
it('creates and edits exact goal DTOs with explicit nullable deadlines and revisions', async () => {
  vi.mocked(teamFetch).mockImplementation(async () => new Response(JSON.stringify({ id: 'g' })))
  await createGoal(bot, fields)
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/b%2F1/goals?scope=team', expect.objectContaining({ method: 'POST', body: JSON.stringify({ ...fields, expected_revision: 0 }) }))
  await editGoal(bot, 'g/2', { ...fields, expected_revision: 3 })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/b%2F1/goals/g%2F2?scope=team', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ ...fields, expected_revision: 3 }) }))
})
it('uses owner confirmation DTO without impersonating an actor or evidence kind', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('{}'))
  await confirmGoal(bot, 'g', { expected_revision: 3, evidence: proof })
  const [, options] = vi.mocked(teamFetch).mock.calls[0]
  expect(JSON.parse(options!.body as string)).toEqual({ expected_revision: 3, evidence: proof })
})
it('lists with cancellation and saves tracked items through PUT with the parent revision and handles 204', async () => {
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('{"items":[]}')).mockResolvedValueOnce(new Response(null, { status: 204 }))
  const signal = new AbortController().signal
  expect(await listTrackedItems(bot, 'g/2', signal)).toEqual({ items: [] })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/b%2F1/goals/g%2F2/tracked-items?scope=team', expect.objectContaining({ signal, method: 'GET' }))
  const body = { expected_goal_revision: 4, title: 'Commitment', status: TrackedStatus.Completed, reason: '', due_at: null, evidence: [proof] }
  expect(await saveTrackedItem(bot, 'g/2', 'i/3', body)).toBeUndefined()
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/b%2F1/goals/g%2F2/tracked-items/i%2F3?scope=team', expect.objectContaining({ method: 'PUT', body: JSON.stringify(body) }))
})
it('preserves conflict status and server detail', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('Revision changed', { status: 409 }))
  await expect(editGoal(bot, 'g', { ...fields, expected_revision: 2 })).rejects.toEqual(new BotAPIError(409, 'Revision changed'))
})
