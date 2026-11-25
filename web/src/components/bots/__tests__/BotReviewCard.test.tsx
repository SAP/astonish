import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { BotReviewCard } from '../BotReviewCard'
import { continueInteractive, approveAndContinue, decideControlProposal, type BotControlProposal } from '@/api/botReviews'
import { BotAPIError, listBotApprovals, type Bot, type BotApproval } from '@/api/bots'
vi.mock('@/api/bots', async original => ({ ...await original<typeof import('@/api/bots')>(), listBotApprovals: vi.fn() }))
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

it.each(['pending', 'approved', 'consumed'] as const)('shows the captured %s action and scope, not private approval internals', status => {
  const exactApproval = { ...approval, status, action: {
    tool: 'journey_exact_action', effect: 'send', recipient: 'owner@example.test',
    arguments: { body: 'exact', token: 'secret-token', nested: { private_checkpoint: 'private-state', safe: 'retained' } },
    checkpoint: 'private-outer-state', target_snapshot: { token: 'private-snapshot' },
  } }
  const view = render(<BotReviewCard bot={bot} approval={exactApproval} readOnly={status === 'consumed'} onChange={vi.fn()} />)
  const details = screen.getByLabelText('Captured action details')
  expect(details.tagName).toBe('PRE')
  for (const text of ['journey_exact_action', 'send', 'owner@example.test', 'exact', 'retained']) expect(details).toHaveTextContent(text)
  expect(view.container).not.toHaveTextContent(/secret-token|private-state|private-outer-state|private-snapshot|Saved action\. Review/)
  view.unmount()
  render(<BotReviewCard bot={bot} approval={exactApproval} readOnly onChange={vi.fn()} />)
  expect(screen.getByLabelText('Captured action details')).toHaveTextContent('exact')
})

it('does not show action arguments on a pending question', () => {
  render(<BotReviewCard bot={bot} approval={{ ...approval, action: { tool: 'ask_user', arguments: { token: 'secret' } }, interaction: { kind: 'chat_question', question_id: 'q', payload: { kind: 'text', prompt: 'Choose a channel' } } }} readOnly={false} onChange={vi.fn()} />)
  expect(screen.queryByLabelText('Captured action details')).not.toBeInTheDocument()
  expect(screen.queryByText('secret')).not.toBeInTheDocument()
})

it('keeps genuine pending permissions reviewable and actionable', () => {
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
  else vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...question, status: 'consumed', response: { question_id: 'exact-q', answer: 'Exact custom answer' } }, run_id: 'r', continuation: 'queued' })
  vi.mocked(continueInteractive).mockResolvedValue({ id: 'r' } as import('@/api/bots').BotRun)
  render(<BotReviewCard bot={activeBot} approval={question} readOnly={false} onChange={vi.fn()} />)
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Exact custom answer' } })
  fireEvent.click(screen.getByRole('button', { name: /submit/i }))
  await waitFor(() => expect(approveAndContinue).toHaveBeenCalledWith(activeBot, question, 'Exact custom answer'))
  expect(continueInteractive).not.toHaveBeenCalled()
  if (expired) {
    fireEvent.click(await screen.findByRole('button', { name: 'Continue interactive' }))
    await waitFor(() => expect(continueInteractive).toHaveBeenCalledWith(activeBot, 'r', expect.any(String), { question_id: 'exact-q', answer: 'Exact custom answer' }))
  } else expect(await screen.findByRole('article', { name: 'Answered question' })).toHaveTextContent('Exact custom answer')
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

const titleQuestion: BotApproval = {
  ...approval, id: 'title-approval', run_id: 'title-run',
  interaction: { kind: 'chat_question', question_id: 'title-question', payload: {
    kind: 'select', prompt: 'What should I do about Boston weather?', allow_custom_answer: true,
    options: [
      { id: 'forecast', label: 'Compare forecasts for Boston this week' },
      { id: 'climate', label: 'Explain typical Boston seasonal weather' },
      { id: 'outing', label: 'Plan a Boston outing around the weather' },
    ],
  } },
}

it.each(['personal', 'team'] as const)('restores the same initial-title review in %s without answering on navigation', async scope => {
  const scopedBot = { ...bot, status: 'draft' as const, scope }
  const onChange = vi.fn()
  const props = { bot: scopedBot, approval: titleQuestion, readOnly: false, onChange }
  const view = render(<BotReviewCard {...props} />)
  expect(screen.getAllByRole('radio')).toHaveLength(3)
  expect(screen.getByRole('article')).toHaveAttribute('data-review-id', 'title-approval')
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Unsaved details' } })
  view.unmount()
  expect(approveAndContinue).not.toHaveBeenCalled()
  expect(continueInteractive).not.toHaveBeenCalled()
  const restored = JSON.parse(JSON.stringify(titleQuestion)) as BotApproval
  render(<BotReviewCard {...props} approval={restored} />)
  expect(screen.getAllByText(titleQuestion.interaction!.payload.prompt)).toHaveLength(1)
  expect(screen.getByRole('article')).toHaveAttribute('data-review-id', 'title-approval')
  vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...restored, status: 'consumed', response: { question_id: 'title-question', answer: 'climate' } }, run_id: 'title-run', continuation: 'queued' })
  fireEvent.click(screen.getByRole('radio', { name: 'Explain typical Boston seasonal weather' }))
  await waitFor(() => expect(approveAndContinue).toHaveBeenCalledExactlyOnceWith(scopedBot, restored, 'climate'))
  expect(vi.mocked(approveAndContinue).mock.calls[0][1].interaction?.question_id).toBe('title-question')
  expect(await screen.findByRole('article', { name: 'Answered question' })).toHaveTextContent('Explain typical Boston seasonal weather')
  expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
})

it.each(['personal', 'team'] as const)('sends only one initial-title decision while %s submission is pending', async scope => {
  let finish!: (result: Awaited<ReturnType<typeof approveAndContinue>>) => void
  vi.mocked(approveAndContinue).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const scopedBot = { ...bot, scope }
  render(<BotReviewCard bot={scopedBot} approval={titleQuestion} readOnly={false} onChange={vi.fn()} />)
  const option = screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' })
  fireEvent.click(option)
  fireEvent.click(option)
  expect(approveAndContinue).toHaveBeenCalledExactlyOnceWith(scopedBot, titleQuestion, 'forecast')
  expect(screen.getByRole('status')).toHaveTextContent('Submitting answer…')
  expect(screen.getByRole('textbox')).toBeDisabled()
  screen.getAllByRole('radio').forEach(control => expect(control).toHaveAttribute('aria-disabled', 'true'))
  await act(async () => { finish({ approval: savedAnswer(), run_id: 'title-run', continuation: 'queued' }) })
  expectCompactAnswer('Compare forecasts for Boston this week')
  expect(screen.queryByRole('radio')).not.toBeInTheDocument()
})

it.each(['personal', 'team'] as const)('retains initial-title custom details after an error in %s and retries exactly', async scope => {
  const scopedBot = { ...bot, scope }
  vi.mocked(approveAndContinue).mockRejectedValueOnce(new Error('Offline')).mockResolvedValueOnce({ approval: savedAnswer('Compare rainfall forecasts for my private trip'), run_id: 'title-run', continuation: 'queued' })
  render(<BotReviewCard bot={scopedBot} approval={titleQuestion} readOnly={false} onChange={vi.fn()} />)
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Compare rainfall forecasts for my private trip' } })
  fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
  await screen.findByRole('alert')
  expect(screen.getByRole('textbox')).toHaveValue('Compare rainfall forecasts for my private trip')
  expect(screen.getAllByRole('radio')).toHaveLength(3)
  fireEvent.click(screen.getByRole('button', { name: 'Submit answer' }))
  await screen.findByRole('article', { name: 'Answered question' })
  expectCompactAnswer('Compare rainfall forecasts for my private trip')
  expect(approveAndContinue).toHaveBeenCalledTimes(2)
  expect(vi.mocked(approveAndContinue).mock.calls[0]).toEqual(vi.mocked(approveAndContinue).mock.calls[1])
  expect(continueInteractive).not.toHaveBeenCalled()
})

it.each(['read-only', 'stopped', 'deleted', 'expired'] as const)('blocks initial-title answers when %s', state => {
  const guardedBot = { ...bot, status: state === 'stopped' || state === 'deleted' ? state : bot.status }
  const guardedQuestion = { ...titleQuestion, expires_at: state === 'expired' ? '2000-01-01' : titleQuestion.expires_at }
  render(<BotReviewCard bot={guardedBot} approval={guardedQuestion} readOnly={state === 'read-only'} onChange={vi.fn()} />)
  screen.getAllByRole('radio').forEach(option => {
    expect(option).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(option)
  })
  expect(screen.getByRole('textbox')).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Submit answer' })).toBeDisabled()
  expect(approveAndContinue).not.toHaveBeenCalled()
  expect(continueInteractive).not.toHaveBeenCalled()
})

it('renders only the existing loading state before the title question is available', () => {
  render(<BotReviewCard bot={bot} readOnly={false} onChange={vi.fn()} />)
  expect(screen.getByRole('status')).toHaveTextContent('Loading saved review…')
  expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  expect(approveAndContinue).not.toHaveBeenCalled()
})

it('shows Thinking until a real approval arrives, then uses standard full-width QuestionCard layout', () => {
  const onChange = vi.fn()
  const question = { ...titleQuestion.interaction!.payload, questionId: 'title-question' }
  const view = render(<BotReviewCard bot={bot} question={question} awaitingApproval readOnly={false} onChange={onChange} />)
  expect(screen.getByRole('status')).toHaveTextContent('Thinking…')
  expect(screen.queryByText('Loading saved review…')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Refresh review' })).not.toBeInTheDocument()
  expect(onChange).toHaveBeenCalledTimes(1)
  expect(approveAndContinue).not.toHaveBeenCalled()
  view.rerender(<BotReviewCard bot={bot} question={question} approval={titleQuestion} awaitingApproval readOnly={false} onChange={onChange} />)
  expect(screen.getByRole('article')).toHaveClass('w-full', 'max-w-2xl')
  expect(screen.getByRole('article')).not.toHaveClass('p-4', 'border')
  expect(screen.getByRole('radiogroup').parentElement?.parentElement?.parentElement).toHaveClass('w-full')
  expect(screen.queryByText('Thinking…')).not.toBeInTheDocument()
})

it('keeps a selected initial-title option bound during explicit reauthentication', async () => {
  vi.mocked(continueInteractive).mockResolvedValue({ id: 'title-run' } as import('@/api/bots').BotRun)
  const view = render(<BotReviewCard bot={bot} approval={titleQuestion} readOnly={false} requiresContinueInteractive onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  const resume = await screen.findByRole('button', { name: 'Continue interactive' })
  expect(approveAndContinue).not.toHaveBeenCalled()
  expect(continueInteractive).not.toHaveBeenCalled()
  view.rerender(<BotReviewCard bot={bot} approval={titleQuestion} readOnly requiresContinueInteractive onChange={vi.fn()} />)
  expect(resume).toBeDisabled()
  fireEvent.click(resume)
  expect(continueInteractive).not.toHaveBeenCalled()
  view.rerender(<BotReviewCard bot={bot} approval={titleQuestion} readOnly={false} requiresContinueInteractive onChange={vi.fn()} />)
  fireEvent.click(resume)
  await waitFor(() => expect(continueInteractive).toHaveBeenCalledWith(bot, 'title-run', expect.any(String), { question_id: 'title-question', answer: 'forecast' }))
})

function savedAnswer(answer = 'forecast', status: BotApproval['status'] = 'consumed') {
  return { ...titleQuestion, status, response: { question_id: 'title-question', answer } }
}
function expectCompactAnswer(answer: string) {
  const card = screen.getByRole('article', { name: 'Answered question' })
  expect(card).toHaveTextContent(titleQuestion.interaction!.payload.prompt)
  expect(card).toHaveTextContent(answer)
  expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /continue/i })).not.toBeInTheDocument()
  expect(card).not.toHaveTextContent(/consumed|approved|queued|haven.t completed|saved your answer/i)
}
it.each(['approved', 'consumed'] as const)('renders a durable %s answer identically after remount', status => {
  const props = { bot, approval: savedAnswer('forecast', status), readOnly: false, onChange: vi.fn() }
  const view = render(<BotReviewCard {...props} />)
  expectCompactAnswer('Compare forecasts for Boston this week')
  expect(screen.queryByText('forecast', { exact: true })).not.toBeInTheDocument()
  view.unmount()
  render(<BotReviewCard {...props} approval={JSON.parse(JSON.stringify(props.approval))} />)
  expectCompactAnswer('Compare forecasts for Boston this week')
})
it('renders durable custom answer literally, not as markup', () => {
  render(<BotReviewCard bot={bot} approval={savedAnswer('<script>private trip</script>')} readOnly onChange={vi.fn()} />)
  expectCompactAnswer('<script>private trip</script>')
})
it('collapses only after successful submission and keeps the answer through stale props', async () => {
  let finish!: (result: Awaited<ReturnType<typeof approveAndContinue>>) => void
  vi.mocked(approveAndContinue).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const props = { bot, approval: titleQuestion, readOnly: false, onChange: vi.fn() }
  const view = render(<BotReviewCard {...props} />)
  fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
  expect(screen.getAllByRole('radio')).toHaveLength(3)
  await act(async () => { finish({ approval: savedAnswer(), run_id: 'title-run', continuation: 'queued' }) })
  expectCompactAnswer('Compare forecasts for Boston this week')
  view.rerender(<BotReviewCard {...props} approval={{ ...titleQuestion, status: 'approved' }} />)
  expectCompactAnswer('Compare forecasts for Boston this week')
  view.rerender(<BotReviewCard {...props} approval={{ ...titleQuestion, id: 'next', interaction: { ...titleQuestion.interaction!, question_id: 'next-question' } }} />)
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
  expect(screen.getAllByRole('radio')).toHaveLength(3)
})
it.each(['missing', 'foreign', 'empty', 'pending', 'denied', 'expired', 'revoked', 'invalid-option'] as const)('does not present %s response as an answered question', state => {
  const saved = savedAnswer()
  if (state === 'missing') Object.assign(saved, { response: undefined })
  else if (state === 'foreign') saved.response.question_id = 'other-question'
  else if (state === 'empty') saved.response.answer = ' '
  else if (state === 'invalid-option') { saved.response.answer = 'unknown-option'; saved.interaction = { ...saved.interaction!, payload: { ...saved.interaction!.payload, allow_custom_answer: false } } }
  else saved.status = state
  render(<BotReviewCard bot={bot} approval={saved} readOnly={false} onChange={vi.fn()} />)
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
})
it.each(['forecast', 'Custom private details'])('refreshes matching durable answer after explicit continuation: %s', async answer => {
  vi.mocked(continueInteractive).mockResolvedValue({ id: 'title-run' } as import('@/api/bots').BotRun)
  vi.mocked(listBotApprovals).mockResolvedValue({ approvals: [savedAnswer(answer)] })
  render(<BotReviewCard bot={bot} approval={titleQuestion} readOnly={false} requiresContinueInteractive onChange={vi.fn()} />)
  if (answer === 'forecast') fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  else { fireEvent.change(screen.getByRole('textbox'), { target: { value: answer } }); fireEvent.click(screen.getByRole('button', { name: 'Submit answer' })) }
  fireEvent.click(await screen.findByRole('button', { name: 'Continue interactive' }))
  await screen.findByRole('article', { name: 'Answered question' })
  expect(listBotApprovals).toHaveBeenCalledWith(bot.id, bot.scope)
  expectCompactAnswer(answer === 'forecast' ? 'Compare forecasts for Boston this week' : answer)
})
it.each(['yes', 'no'] as const)('renders durable yes/no answer as a label: %s', answer => {
  render(<BotReviewCard bot={bot} approval={{ ...savedAnswer(answer), interaction: { ...titleQuestion.interaction!, payload: { kind: 'yesno', prompt: titleQuestion.interaction!.payload.prompt } } }} readOnly={false} onChange={vi.fn()} />)
  expectCompactAnswer(answer === 'yes' ? 'Yes' : 'No')
})
it('does not collapse an unconfirmed submission response', async () => {
  vi.mocked(approveAndContinue).mockResolvedValue({ approval: { ...titleQuestion, status: 'consumed' }, run_id: 'title-run', continuation: 'queued' })
  render(<BotReviewCard bot={bot} approval={titleQuestion} readOnly={false} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  await screen.findByRole('alert')
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
  expect(screen.getAllByRole('radio')).toHaveLength(3)
})
it('never applies a previous question authorization answer to a new checkpoint', async () => {
  const props = { bot, approval: titleQuestion, readOnly: false, requiresContinueInteractive: true, onChange: vi.fn() }
  const view = render(<BotReviewCard {...props} />)
  fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  await screen.findByRole('button', { name: 'Continue interactive' })
  view.rerender(<BotReviewCard {...props} approval={{ ...titleQuestion, id: 'new-approval', interaction: { ...titleQuestion.interaction!, question_id: 'new-question' } }} />)
  expect(screen.queryByRole('button', { name: 'Continue interactive' })).not.toBeInTheDocument()
  expect(continueInteractive).not.toHaveBeenCalled()
})
it('retains failed continuation answer for retry and rejects foreign refreshed approvals', async () => {
  vi.mocked(continueInteractive).mockRejectedValueOnce(new Error('private diagnostic')).mockResolvedValue({ id: 'title-run' } as import('@/api/bots').BotRun)
  vi.mocked(listBotApprovals).mockResolvedValueOnce({ approvals: [{ ...savedAnswer(), run_id: 'foreign-run' }] }).mockResolvedValue({ approvals: [savedAnswer()] })
  render(<BotReviewCard bot={bot} approval={titleQuestion} readOnly={false} requiresContinueInteractive onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('radio', { name: 'Compare forecasts for Boston this week' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Continue interactive' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Continue interactive' })).toBeEnabled())
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
  expect(screen.queryByText(/private diagnostic/)).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Continue interactive' }))
  await waitFor(() => expect(listBotApprovals).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('article', { name: 'Answered question' })).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Continue interactive' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Continue interactive' }))
  await screen.findByRole('article', { name: 'Answered question' })
  expectCompactAnswer('Compare forecasts for Boston this week')
  expect(vi.mocked(continueInteractive).mock.calls[0]).toEqual(vi.mocked(continueInteractive).mock.calls[2])
})
