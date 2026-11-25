import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { BotAPIError, BotStatus, decideBotApproval, listBotApprovals, type Bot, type BotApproval } from '@/api/bots'
import { QUESTION_KINDS, validTextAnswer } from '@/api/questions'
import { continueInteractive, approveAndContinue, decideControlProposal, ProposalStatus, type BotControlProposal } from '@/api/botReviews'
import QuestionCard from '../chat/questions/QuestionCard'
import BotEmployeeMessage from './BotEmployeeMessage'
import { botEmployeeFeedback, botLocalFailure, BotFailureKind } from '../chat/botEmployeeFeedback'
import { useBotClock } from './useBotClock'

const AnsweredStatus = { Approved: 'approved', Consumed: 'consumed' } as const satisfies Record<string, BotApproval['status']>

function sameApproval(left?: BotApproval, right?: BotApproval): boolean {
  return !!left && !!right && left.id === right.id && left.run_id === right.run_id && left.action_digest === right.action_digest && left.interaction?.question_id === right.interaction?.question_id
}

/** Display only the persisted answer, never an in-flight selection or queue receipt. */
function savedAnswerLabel(approval?: BotApproval): string | undefined {
  const interaction = approval?.interaction, response = approval?.response
  if (!interaction || !response || (approval.status !== AnsweredStatus.Approved && approval.status !== AnsweredStatus.Consumed) || response.question_id !== interaction.question_id || typeof response.answer !== 'string' || !validTextAnswer(response.answer)) return
  const { payload } = interaction
  if (payload.kind === QUESTION_KINDS.yesno) return response.answer === 'yes' ? 'Yes' : response.answer === 'no' ? 'No' : undefined
  if (payload.kind === QUESTION_KINDS.select) return payload.options?.find(option => option.id === response.answer)?.label ?? (payload.allow_custom_answer ? response.answer : undefined)
  if (payload.kind === QUESTION_KINDS.text) return response.answer
}

// Presentation only: the server's saved descriptor/digest remains authoritative.
// Never serialize the approval, checkpoint or arbitrary private descriptor fields.
function capturedActionDetails(action: BotApproval['action']): string {
  const publicFields = ['tool', 'effect', 'arguments', 'recipient', 'merchant', 'currency', 'spend_minor', 'target_fingerprint', 'target_snapshot'] as const
  const details = Object.fromEntries(publicFields.filter(key => action[key] !== undefined).map(key => [key, action[key]]))
  return JSON.stringify(details, (key, value: unknown) => {
    if (/secret|token|password|credential|authorization|checkpoint|private/i.test(key)) return undefined
    if (typeof value === 'string' && value.includes('SECRET_')) return '[redacted]'
    return value
  }, 2)
}

/** Shared by the canonical transcript card and the optional recovery inbox. */
export function BotReviewCard({ bot, proposal, approval, question, awaitingApproval = false, readOnly, onChange, requiresContinueInteractive = false }: { bot: Bot; proposal?: BotControlProposal; approval?: BotApproval; question?: import('@/api/questions').Question; awaitingApproval?: boolean; requiresContinueInteractive?: boolean; readOnly: boolean; onChange: () => void }) {
  const now = useBotClock()
  const [pendingAnswer, setPendingAnswer] = useState<string>()
  const [needsContinue, setNeedsContinue] = useState(false)
  const continuationKey = useRef(crypto.randomUUID())
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [receipt, setReceipt] = useState('')
  const [decidedProposal, setDecidedProposal] = useState<BotControlProposal>()
  const [decidedApproval, setDecidedApproval] = useState<BotApproval>()
  const p = decidedProposal?.id === proposal?.id && proposal?.status === ProposalStatus.Proposed ? decidedProposal : proposal
  const a = sameApproval(decidedApproval, approval) && (approval?.status === 'pending' || approval?.status === AnsweredStatus.Approved) ? decidedApproval : approval
  const act = async (work: () => Promise<void>) => {
    if (busy || readOnly) return
    setBusy(true); setError('')
    try { await work(); onChange() } catch (e) { setError(botLocalFailure(e, BotFailureKind.Decision).message); onChange() } finally { setBusy(false) }
  }
  useEffect(() => {
    setPendingAnswer(undefined)
    setNeedsContinue(false)
    setReceipt('')
    setError('')
    continuationKey.current = crypto.randomUUID()
  }, [bot.id, bot.scope, approval?.id, approval?.run_id, approval?.action_digest, approval?.interaction?.question_id])
  const refreshRef = useRef(onChange)
  refreshRef.current = onChange
  useEffect(() => { if ((question || awaitingApproval) && !approval) refreshRef.current() }, [question?.questionId, awaitingApproval, !!approval])
  if (!p && !a && (question || awaitingApproval)) return <p role="status" className="my-3 text-sm text-muted-foreground">Thinking…</p>
  if (!p && !a) return <article aria-label="Bot review" className="my-3 border rounded-lg p-4"><p role="status">Loading saved review…</p><Button variant="ghost" onClick={onChange}>Refresh review</Button></article>
  const answerLabel = savedAnswerLabel(a)
  if (!p && a?.interaction && answerLabel !== undefined) return <article aria-label="Answered question" data-review-id={a.id} className="my-3 w-full max-w-2xl rounded-lg border border-border bg-card p-3 text-sm text-card-foreground">
    <dl className="space-y-1">
      <dt className="font-medium whitespace-pre-wrap break-words">{a.interaction.payload.prompt}</dt>
      <dd className="whitespace-pre-wrap break-words"><span className="text-muted-foreground">Your answer: </span>{answerLabel}</dd>
    </dl>
  </article>
  const saveAnswer = (saved: BotApproval, expected: BotApproval, answer: string) => {
    if (!sameApproval(saved, expected) || saved.response?.answer !== answer || savedAnswerLabel(saved) === undefined) throw new Error('Saved answer not confirmed')
    setDecidedApproval(saved)
    setNeedsContinue(false)
  }
  const expired = Date.parse((p || a)!.expires_at) <= now
  const disabled = readOnly || busy || expired || bot.status === BotStatus.Stopped || bot.status === BotStatus.Deleted
  const approve = (answer?: string) => act(async () => {
    const result = await approveAndContinue(bot, a!, answer)
    setDecidedApproval(result.approval)
    setReceipt(botEmployeeFeedback({ kind: 'decision_saved', decision: 'approval', continuation: result.continuation }))
  })
  return <article aria-label="Bot review" data-review-id={(p || a)!.id} className={a?.interaction ? 'my-3 w-full max-w-2xl space-y-3 text-sm' : 'my-3 border border-border rounded-lg p-4 space-y-3 text-sm'}>
    {(p || !a?.interaction || a.status !== 'pending') && <h3 className="font-semibold">{p ? `Review ${p.payload.operation.replaceAll('_', ' ')}` : a!.interaction?.payload.prompt || 'Permission to perform this exact action'}</h3>}
    {p ? <><BotEmployeeMessage>{botEmployeeFeedback({ kind: 'review_required' })}</BotEmployeeMessage><p className="whitespace-pre-wrap break-words">{'task_once' in p.payload ? p.payload.task_once.instructions : 'responsibility' in p.payload ? p.payload.responsibility : 'schedule' in p.payload ? p.payload.schedule.trigger.instructions : 'goal' in p.payload ? p.payload.goal.goal.title : 'memory' in p.payload ? p.payload.memory.content || 'Saved memory change' : 'Saved configuration change'}</p><p>{expired && p.status === ProposalStatus.Proposed ? 'expired' : p.status}{p.reconciliation_pending ? ' · reconciliation pending' : ''}</p>
      {p.status === ProposalStatus.Proposed && <div className="flex flex-wrap gap-2"><Button disabled={disabled} onClick={() => act(async () => { setDecidedProposal(await decideControlProposal(bot, p, 'confirm')) })}>Confirm exact change</Button><Button variant="outline" disabled={disabled} onClick={() => act(async () => { setDecidedProposal(await decideControlProposal(bot, p, 'reject')) })}>Reject</Button></div>}
      {p.reconciliation_pending && <BotEmployeeMessage role="status">{botEmployeeFeedback({ kind: 'reconciliation_pending' })}</BotEmployeeMessage>}
      {(p.status === ProposalStatus.Stale || p.status === ProposalStatus.Expired || expired) && <BotEmployeeMessage>{botEmployeeFeedback({ kind: 'fresh_proposal_required' })}</BotEmployeeMessage>}
    </> : <>{!a!.interaction && <>{(typeof a!.action.summary === 'string' || typeof a!.action.description === 'string') && <p className="whitespace-pre-wrap break-words">{typeof a!.action.summary === 'string' ? a!.action.summary : a!.action.description as string}</p>}<pre aria-label="Captured action details" tabIndex={0} className="max-h-80 overflow-auto rounded-md border border-border bg-muted p-3 whitespace-pre-wrap break-words text-xs">{capturedActionDetails(a!.action)}</pre></>}{(!a!.interaction || a!.status !== 'pending' || expired) && <p>{expired && a!.status === 'pending' ? 'expired' : a!.status}</p>}
      {a!.status === 'pending' && <div className={a!.interaction ? 'w-full' : 'flex flex-wrap gap-2'}>{a!.interaction ? <QuestionCard key={`${a!.id}/${a!.interaction.question_id}`} question={{ ...a!.interaction.payload, questionId: a!.interaction.question_id }} disabled={disabled} onAnswer={async ({ answer }) => { setPendingAnswer(answer); if (requiresContinueInteractive) { setNeedsContinue(true); throw new Error(botEmployeeFeedback({ kind: 'answer_authorization_required' })) } try { const result = await approveAndContinue(bot, a!, answer); saveAnswer(result.approval, a!, answer); onChange() } catch (e) { if (e instanceof BotAPIError && e.status === 403) setNeedsContinue(true); throw new Error(botLocalFailure(e, BotFailureKind.Decision).message, { cause: e }) } }} /> : <Button disabled={disabled} onClick={() => approve()}>Approve & continue</Button>}{!a!.interaction && <Button variant="outline" disabled={disabled} onClick={() => act(async () => { setDecidedApproval(await decideBotApproval(bot, a!, 'denied')) })}>Deny</Button>}</div>}
      {a!.status === 'approved' && !receipt && <><BotEmployeeMessage>{botEmployeeFeedback({ kind: 'approved_checkpoint', paused: bot.status === BotStatus.Paused })}</BotEmployeeMessage><Button disabled={disabled || bot.status === BotStatus.Paused} onClick={() => approve()}>Continue approved action</Button></>}
    </>}
    {needsContinue && pendingAnswer !== undefined && a?.interaction && <div><BotEmployeeMessage>{botEmployeeFeedback({ kind: 'answer_authorization_required' })}</BotEmployeeMessage><Button disabled={disabled} onClick={() => act(async () => { await continueInteractive(bot, a.run_id, continuationKey.current, { question_id: a.interaction!.question_id, answer: pendingAnswer }); const { approvals } = await listBotApprovals(bot.id, bot.scope); const saved = approvals.find(candidate => sameApproval(candidate, a)); if (!saved) throw new Error('Saved question not found'); saveAnswer(saved, a, pendingAnswer); })}>Continue interactive</Button></div>}
    {readOnly && <BotEmployeeMessage>{botEmployeeFeedback({ kind: 'read_only' })}</BotEmployeeMessage>}{receipt && <BotEmployeeMessage role="status">{receipt}</BotEmployeeMessage>}{error && <BotEmployeeMessage role="alert">{error}</BotEmployeeMessage>}
  </article>
}
