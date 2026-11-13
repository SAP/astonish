import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { BotReviewCard } from '../BotReviewCard'
import { continueInteractive, approveAndContinue, decideControlProposal, type BotControlProposal } from '@/api/botReviews'
import { BotAPIError, type Bot, type BotApproval } from '@/api/bots'
vi.mock('@/api/botReviews', async original => ({ ...await original<typeof import('@/api/botReviews')>(), continueInteractive: vi.fn(), approveAndContinue: vi.fn(), decideControlProposal: vi.fn() }))
const bot = { id: 'bot', scope: 'personal', status: 'paused', version: 1 } as Bot
const proposal: BotControlProposal = { id: 'p', bot_id: 'bot', source_run_id: 'r', source_user_id: 'u', version: 1, expected_revision: 1, digest: 'exact', payload: { operation: 'resume', reason: 'Owner requested resume' }, status: 'proposed', created_at: '', expires_at: '2099-01-01', reconciliation_pending: false }
const approval: BotApproval = { id: 'a', run_id: 'r', version: 1, action_digest: 'digest', action: { tool: 'email' }, status: 'pending', expires_at: '2099-01-01' }
it('renders the Boston pending question once without permission chrome', () => {
  const prompt = 'Who should receive the Boston weather updates, and via which channel? (e.g., "just me via Telegram", "email to me", etc.)'
  render(<BotReviewCard bot={bot} approval={{ ...approval, interaction: { kind: 'chat_question', question_id: 'q-4180aeb8099e', payload: { kind: 'text', prompt } } }} readOnly={false} onChange={vi.fn()} />)
  expect(screen.getAllByText(prompt)).toHaveLength(1)
  expect(screen.queryByText('pending')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Deny' })).not.toBeInTheDocument()
})

it('keeps genuine pending permissions generic and actionable', () => {
  render(<BotReviewCard bot={bot} approval={approval} readOnly={false} onChange={vi.fn()} />)
  expect(screen.getByRole('heading', { name: 'Permission to perform this exact action' })).toBeVisible()
  expect(screen.getByText('pending')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Deny' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Approve & continue' })).toBeEnabled()
})

beforeEach(() => vi.clearAllMocks())
it.each([false, true])('ordinary answer uses existing authority; expired=%s needs an explicit second click', async expired => {
  const activeBot = { ...bot, status: 'draft' as const }
  const question = { ...approval, interaction: { kind: 'chat_question' as const, question_id: 'exact-q', payload: { kind: 'text' as const, prompt: 'Specific question' } } }
  if (expired) vi.mocked(approveAndContinue).mockRejectedValue(new BotAPIError(403, 'Authority expired'))
  else vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...question, status: 'consumed' }, run_id: 'r', continuation: 'queued' })
  vi.mocked(continueInteractive).mockResolvedValue({ id: 'r' } as import('@/api/bots').BotRun)
  render(<BotReviewCard bot={activeBot} approval={question} readOnly={false} onChange={vi.fn()} />)
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Exact custom answer' } })
  fireEvent.click(screen.getByRole('button', { name: /submit/i }))
  await waitFor(() => expect(approveAndContinue).toHaveBeenCalledWith(activeBot, question, 'Exact custom answer'))
  expect(continueInteractive).not.toHaveBeenCalled()
  if (expired) {
    fireEvent.click(await screen.findByRole('button', { name: 'Continue interactive' }))
    await waitFor(() => expect(continueInteractive).toHaveBeenCalledWith(activeBot, 'r', expect.any(String), { question_id: 'exact-q', answer: 'Exact custom answer' }))
  } else expect(await screen.findByRole('status')).toHaveTextContent('I saved your answer and queued the continuation.')
})
it('confirms only the saved digest and retains the terminal receipt before refresh', async () => {
  vi.mocked(decideControlProposal).mockResolvedValue({ ...proposal, status: 'applied' })
  render(<BotReviewCard bot={bot} proposal={proposal} readOnly={false} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Confirm exact change' }))
  await screen.findByText('applied')
  expect(decideControlProposal).toHaveBeenCalledWith(bot, proposal, 'confirm')
  expect(screen.queryByRole('button', { name: 'Confirm exact change' })).not.toBeInTheDocument()
})
it.each(['stale', 'expired', 'rejected', 'applied'] as const)('does not reauthorize a %s proposal', status => {
  render(<BotReviewCard bot={bot} proposal={{ ...proposal, status }} readOnly={false} onChange={vi.fn()} />)
  expect(screen.queryByRole('button', { name: 'Confirm exact change' })).not.toBeInTheDocument()
})
it('prevents nonowner decisions', () => {
  render(<BotReviewCard bot={bot} proposal={proposal} readOnly onChange={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Confirm exact change' })).toBeDisabled()
})
it('approves while paused without claiming execution or resuming', async () => {
  vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...approval, status: 'approved' }, run_id: 'r', continuation: 'blocked_by_pause' })
  render(<BotReviewCard bot={bot} approval={approval} readOnly={false} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Approve & continue' }))
  expect(await screen.findByRole('status')).toHaveTextContent('I’m still paused')
  expect(approveAndContinue).toHaveBeenCalledWith(bot, approval, undefined)
})
it('uses durable option identity for an answer and retains errors for exact retries', async () => {
  const question = { ...approval, interaction: { kind: 'chat_question' as const, question_id: 'q', payload: { kind: 'select' as const, prompt: 'Choose', options: [{ id: 'one', label: 'First' }] } } }
  vi.mocked(approveAndContinue).mockRejectedValue(new Error('secret /api/private diagnostic', { cause: new Error('secret nested cause') }))
  render(<BotReviewCard bot={bot} approval={question} readOnly={false} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('radio', { name: 'First' }))
  await waitFor(() => expect(approveAndContinue).toHaveBeenCalledWith(bot, question, 'one'))
  expect(await screen.findByRole('alert')).toHaveTextContent('I can’t confirm whether your decision was saved')
  expect(screen.getByRole('alert')).not.toHaveTextContent(/secret|\/api\/private|nested cause/)
  expect(screen.queryByText(/secret|\/api\/private|nested cause/)).not.toBeInTheDocument()
})

it('requires a separate explicit fresh authorization click and retains the exact custom answer', async () => {
  const question = { ...approval, interaction: { kind: 'chat_question' as const, question_id: 'q', payload: { kind: 'text' as const, prompt: 'What should I do?' } } }
  vi.mocked(continueInteractive).mockResolvedValue({ id: 'r' } as import('@/api/bots').BotRun)
  render(<BotReviewCard bot={bot} approval={question} readOnly={false} requiresContinueInteractive onChange={vi.fn()} />)
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Use this custom answer' } })
  fireEvent.click(screen.getByRole('button', { name: /submit/i }))
  const button = await screen.findByRole('button', { name: 'Continue interactive' })
  expect(approveAndContinue).not.toHaveBeenCalled()
  expect(continueInteractive).not.toHaveBeenCalled()
  fireEvent.click(button)
  await waitFor(() => expect(continueInteractive).toHaveBeenCalledWith(bot, 'r', expect.any(String), { question_id: 'q', answer: 'Use this custom answer' }))
})
