import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { BotAPIError, BotStatus, decideBotApproval, type Bot, type BotApproval } from '@/api/bots'
import { continueInteractive, approveAndContinue, Continuation, decideControlProposal, ProposalStatus, type BotControlProposal } from '@/api/botReviews'
import QuestionCard from '../chat/questions/QuestionCard'
import { useBotClock } from './useBotClock'

/** Shared by the canonical transcript card and the optional recovery inbox. */
export function BotReviewCard({ bot, proposal, approval, readOnly, onChange, requiresContinueInteractive = false }: { bot: Bot; proposal?: BotControlProposal; approval?: BotApproval; requiresContinueInteractive?: boolean; readOnly: boolean; onChange: () => void }) {
  const now = useBotClock()
  const [pendingAnswer, setPendingAnswer] = useState<string>()
  const [needsContinue, setNeedsContinue] = useState(false)
  const continuationKey = useRef(crypto.randomUUID())
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [receipt, setReceipt] = useState('')
  const [decidedProposal, setDecidedProposal] = useState<BotControlProposal>()
  const [decidedApproval, setDecidedApproval] = useState<BotApproval>()
  const p = decidedProposal?.id === proposal?.id && proposal?.status === ProposalStatus.Proposed ? decidedProposal : proposal
  const a = decidedApproval?.id === approval?.id && (approval?.status === 'pending' || approval?.status === 'approved') ? decidedApproval : approval
  const act = async (work: () => Promise<void>) => {
    if (busy || readOnly) return
    setBusy(true); setError('')
    try { await work(); onChange() } catch (e) { setError(e instanceof Error ? e.message : String(e)); onChange() } finally { setBusy(false) }
  }
  if (!p && !a) return <article aria-label="Bot review" className="my-3 border rounded-lg p-4"><p role="status">Loading saved review…</p><Button variant="ghost" onClick={onChange}>Refresh review</Button></article>
  const expired = Date.parse((p || a)!.expires_at) <= now
  const disabled = readOnly || busy || expired || bot.status === BotStatus.Stopped || bot.status === BotStatus.Deleted
  const approve = (answer?: string) => act(async () => {
    const result = await approveAndContinue(bot, a!, answer)
    setDecidedApproval(result.approval)
    setReceipt(result.continuation === Continuation.Blocked ? 'Approved; continuation blocked by pause. The Bot remains paused.' : 'Approved; continuation queued. Execution is not yet complete.')
  })
  return <article aria-label="Bot review" data-review-id={(p || a)!.id} className="my-3 border border-border rounded-lg p-4 space-y-3 text-sm">
    {(p || !a?.interaction || a.status !== 'pending') && <h3 className="font-semibold">{p ? `Review ${p.payload.operation.replaceAll('_', ' ')}` : a!.interaction?.payload.prompt || 'Permission to perform this exact action'}</h3>}
    {p ? <><p>{p.payload.reason}</p><p className="whitespace-pre-wrap break-words">{'task_once' in p.payload ? p.payload.task_once.instructions : 'responsibility' in p.payload ? p.payload.responsibility : 'schedule' in p.payload ? p.payload.schedule.trigger.instructions : 'goal' in p.payload ? p.payload.goal.goal.title : 'memory' in p.payload ? p.payload.memory.content || 'Saved memory change' : 'Saved configuration change'}</p><p>{expired && p.status === ProposalStatus.Proposed ? 'expired' : p.status}{p.reconciliation_pending ? ' · reconciliation pending' : ''}</p>
      {p.status === ProposalStatus.Proposed && <div className="flex flex-wrap gap-2"><Button disabled={disabled} onClick={() => act(async () => { setDecidedProposal(await decideControlProposal(bot, p, 'confirm')) })}>Confirm exact change</Button><Button variant="outline" disabled={disabled} onClick={() => act(async () => { setDecidedProposal(await decideControlProposal(bot, p, 'reject')) })}>Reject</Button></div>}
      {p.reconciliation_pending && <p role="status">Saved. Reconciliation is pending; do not submit a replacement.</p>}
      {(p.status === ProposalStatus.Stale || p.status === ProposalStatus.Expired || expired) && <p>Ask in Chat for a fresh proposal. This saved review cannot authorize a changed action.</p>}
    </> : <>{!a!.interaction && <p className="whitespace-pre-wrap break-words">{typeof a!.action.summary === 'string' ? a!.action.summary : typeof a!.action.description === 'string' ? a!.action.description : 'Saved action. Review its scope before continuing.'}</p>}{(!a!.interaction || a!.status !== 'pending' || expired) && <p>{expired && a!.status === 'pending' ? 'expired' : a!.status}</p>}
      {a!.status === 'pending' && <div className="flex flex-wrap gap-2">{a!.interaction ? <QuestionCard key={`${a!.id}/${a!.interaction.question_id}`} question={{ ...a!.interaction.payload, questionId: a!.interaction.question_id }} disabled={disabled} onAnswer={async ({ answer }) => { setPendingAnswer(answer); if (requiresContinueInteractive) { setNeedsContinue(true); throw new Error('Fresh authorization required. Select Continue interactive to submit this answer.') } try { const result = await approveAndContinue(bot, a!, answer); setDecidedApproval(result.approval); setReceipt(result.continuation === Continuation.Blocked ? 'Approved; continuation blocked by pause. The Bot remains paused.' : 'Answer saved; continuation queued.'); onChange() } catch (e) { if (e instanceof BotAPIError && e.status === 403) setNeedsContinue(true); throw e } }} /> : <Button disabled={disabled} onClick={() => approve()}>Approve & continue</Button>}{!a!.interaction && <Button variant="outline" disabled={disabled} onClick={() => act(async () => { setDecidedApproval(await decideBotApproval(bot, a!, 'denied')) })}>Deny</Button>}</div>}
      {a!.status === 'approved' && !receipt && <><p>{bot.status === BotStatus.Paused ? 'Approved; continuation blocked by pause. Resume requires a separate review.' : 'Approved. Continue only the saved checkpoint.'}</p><Button disabled={disabled || bot.status === BotStatus.Paused} onClick={() => approve()}>Continue approved action</Button></>}
    </>}
    {needsContinue && pendingAnswer !== undefined && a?.interaction && <div><p>Fresh authorization is required. Your answer has not been submitted. Continue this saved question explicitly.</p><Button disabled={disabled} onClick={() => act(async () => { await continueInteractive(bot, a.run_id, continuationKey.current, { question_id: a.interaction!.question_id, answer: pendingAnswer }); setNeedsContinue(false); setReceipt('Answer saved; continuation queued.'); })}>Continue interactive</Button></div>}
    {readOnly && <p className="text-muted-foreground">Read-only review. The owner must decide.</p>}{receipt && <p role="status">{receipt}</p>}{error && <p role="alert">{error} Refresh before retrying; the saved digest is unchanged.</p>}
  </article>
}
