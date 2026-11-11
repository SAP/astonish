import { useState } from 'react'
import { Button } from '@/components/ui/button'
import type { BotScope } from '@/api/bots'
import { decideBotOperation } from '@/api/botReviews'
import { OperationDecision, OperationPresentation, OperationStatus, operationLabel, type BotOperationView } from '@/api/botOperationResult'
import BotEmployeeMessage from './BotEmployeeMessage'
import { botLocalFailure, BotFailureKind } from '../chat/botEmployeeFeedback'
import { useBotClock } from './useBotClock'

const labels = { pending: 'Pending', applying: 'Applying', applied: 'Applied', blocked: 'Blocked', failed: 'Failed', cancelled: 'Cancelled' }
export default function BotOperationResult({ operation, scope, readOnly, onChange }: { operation: BotOperationView; scope: BotScope; readOnly: boolean; onChange: (next: BotOperationView) => void }) {
  const now = useBotClock()
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const label = operationLabel(operation.operation)
  const exception = operation.exception
  const needsDecision = operation.status === OperationStatus.Blocked && operation.decision === OperationDecision.RequireAuthorization
  const fields = (Array.isArray(exception?.fields) ? exception.fields : []).filter(field => typeof field.caption === 'string' && typeof field.value === 'string' && field.caption !== 'Change' && field.value.trim())
  const meaningfulScope = !!label && fields.length > 0 && exception?.operation_id === operation.operation_id && !!exception.payload_digest
  const expired = !exception || !Number.isFinite(Date.parse(exception.expires_at)) || Date.parse(exception.expires_at) <= now
  const decide = async (approve: boolean) => {
    if (busy || readOnly || expired || !meaningfulScope) return
    setBusy(true); setError('')
    try { onChange(await decideBotOperation({ id: operation.bot_id, scope }, operation, approve)) }
    catch (e) { setError(botLocalFailure(e, BotFailureKind.Decision).message) }
    finally { setBusy(false) }
  }
  // Conversation publications belong to StudioChat's ordinary agent renderer.
  // This leaf owns only legacy results and actionable authorization cards.
  if (!needsDecision && (operation.presentation === OperationPresentation.Internal || operation.presentation === OperationPresentation.Conversation)) return null
  const schedule = operation.status === OperationStatus.Applied ? operation.verified_schedule : undefined
  return <article aria-label="Operation result" data-operation-id={operation.operation_id} className="my-3 border border-border rounded-lg p-4 space-y-2 text-sm">
    <h3 className="font-semibold">{label || 'Operation'} · {labels[operation.status]}</h3>
    <BotEmployeeMessage>{needsDecision ? "I need your permission for this exact saved action. Please review its scope below." : operation.summary}</BotEmployeeMessage>
    {!needsDecision && operation.blocker && <BotEmployeeMessage>{operation.blocker}</BotEmployeeMessage>}
    {(operation.status === OperationStatus.Pending || operation.status === OperationStatus.Applying) && <p role="status">I haven’t verified an applied result yet.</p>}
    {schedule && <p>{schedule.removed ? 'Schedule removed.' : schedule.enabled ? 'Schedule enabled.' : 'Schedule paused.'}{!schedule.removed && schedule.enabled && schedule.next_run ? ` Next run: ${schedule.next_run}` : ''}</p>}
    {needsDecision && fields.length > 0 && <dl>{fields.map((field, index) => <div key={index}><dt className="font-medium">{field.caption}</dt><dd className="whitespace-pre-wrap break-words">{field.value}</dd></div>)}</dl>}
    {needsDecision && <>{meaningfulScope ? <><p>I’m asking for permission for this saved action only.</p><dl><dt className="font-medium">Expires</dt><dd className="break-words"><time dateTime={exception!.expires_at}>{exception!.expires_at}</time></dd></dl>{!readOnly && !expired && <div className="flex gap-2"><Button disabled={busy || !!error} onClick={() => void decide(true)}>Approve {label?.toLowerCase()}</Button><Button variant="outline" disabled={busy || !!error} onClick={() => void decide(false)}>Deny</Button></div>}{expired && <p>I need a fresh request because this one expired.</p>}</> : <p>I can’t show the action details. Please ask for a fresh request before authorizing.</p>}{readOnly && <p>I need the owner to decide; this review is read-only.</p>}</>}
    {error && <BotEmployeeMessage role="alert">{error}</BotEmployeeMessage>}
  </article>
}
