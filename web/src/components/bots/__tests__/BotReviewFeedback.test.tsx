import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { BotReviewCard } from '../BotReviewCard'
import { approveAndContinue, Continuation } from '@/api/botReviews'
import type { Bot, BotApproval } from '@/api/bots'
import { botEmployeeFeedback } from '../../chat/botEmployeeFeedback'

vi.mock('@/api/botReviews', async original => ({ ...await original<typeof import('@/api/botReviews')>(), approveAndContinue: vi.fn() }))
const bot = { id: 'bot', scope: 'personal', status: 'paused', version: 1 } as Bot
const approval: BotApproval = { id: 'a', run_id: 'r', version: 1, action_digest: 'exact-digest', action: { summary: 'Send only this email' }, status: 'pending', expires_at: '2099-01-01' }
beforeEach(() => vi.clearAllMocks())

it.each([Continuation.Blocked, Continuation.Queued])('uses shared answer receipt for %s without rewriting the question', async continuation => {
  const prompt = 'Where should I send this exact report?'
  const question: BotApproval = { ...approval, interaction: { kind: 'chat_question', question_id: 'exact-q', payload: { kind: 'text', prompt } } }
  vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...question, status: 'consumed' }, run_id: 'r', continuation })
  render(<BotReviewCard bot={bot} approval={question} readOnly={false} onChange={vi.fn()} />)
  expect(screen.getAllByText(prompt)).toHaveLength(1)
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Only me' } })
  fireEvent.click(screen.getByRole('button', { name: /submit/i }))
  const receipt = (await screen.findByText(botEmployeeFeedback({ kind: 'decision_saved', decision: 'answer', continuation }))).closest('[role="status"]')
  expect(receipt).toHaveClass('markdown-body')
  expect(receipt).toHaveTextContent(botEmployeeFeedback({ kind: 'decision_saved', decision: 'answer', continuation }))
  expect(screen.getAllByText(prompt)).toHaveLength(1)
  expect(approveAndContinue).toHaveBeenCalledExactlyOnceWith(bot, question, 'Only me')
})

it.each([401, 403, 409, 500])('keeps exact permission controls and hides diagnostics for %s', async status => {
  vi.mocked(approveAndContinue).mockRejectedValue({ status, message: 'secret HTTP /api/private' })
  render(<BotReviewCard bot={bot} approval={approval} readOnly={false} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Approve & continue' }))
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveClass('markdown-body')
  expect(alert).not.toHaveTextContent(/secret|HTTP|\/api/)
  expect(screen.getByText('Send only this email')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Deny' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Approve & continue' })).toBeEnabled()
  expect(approveAndContinue).toHaveBeenCalledExactlyOnceWith(bot, approval, undefined)
})
