import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Bot } from '@/api/bots'
import { continueInteractive } from '@/api/botReviews'
import { botEmployeeFeedback, botLocalFailure } from '../chat/botEmployeeFeedback'
import BotEmployeeMessage from './BotEmployeeMessage'

import { getActiveTeam } from '@/api/teamContext'
type Props = { bot: Bot; runId: string; checkpointRevision?: number; disabled: boolean; onChange: () => void }
export default function BotContinueInteractive(props: Props) {
  return <Continuation key={`${getActiveTeam() || ''}/${props.bot.scope}/${props.bot.id}/${props.runId}/${props.checkpointRevision || ''}`} {...props} />
}
function Continuation({ bot, runId, checkpointRevision, disabled, onChange }: Props) {
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const key = useRef(crypto.randomUUID())
  const lock = useRef(false)
  const [pending, setPending] = useState(false)
  const [accepted, setAccepted] = useState(false)
  const [error, setError] = useState('')
  const resume = async () => {
    if (disabled || lock.current || accepted) return
    lock.current = true; setPending(true); setError('')
    try { await (checkpointRevision ? continueInteractive(bot, runId, key.current, { kind: 'progress_v1', checkpoint_revision: checkpointRevision }) : continueInteractive(bot, runId, key.current)); if (alive.current) { setAccepted(true); onChange() } }
    catch (e) { if (alive.current) setError(botLocalFailure(e).message) }
    finally { lock.current = false; if (alive.current) setPending(false) }
  }
  return <section aria-label={`Continue run ${runId}`} className="border-b border-border px-4 py-3 text-sm">
    <BotEmployeeMessage>{checkpointRevision ? `Continue saved progress at checkpoint ${checkpointRevision}. This does not change lifecycle or enable schedules.` : botEmployeeFeedback({ kind: 'continuation_authorization_required' })}</BotEmployeeMessage>
    <Button disabled={disabled || pending || accepted} onClick={() => { void resume() }}>{accepted ? 'Continuation requested' : pending ? 'Continuing…' : checkpointRevision ? 'Continue saved progress' : 'Continue interactive'}</Button>
    {error && <BotEmployeeMessage role="alert">{error}</BotEmployeeMessage>}
  </section>
}
