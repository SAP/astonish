import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Bot } from '@/api/bots'
import { continueInteractive } from '@/api/botReviews'
import { botEmployeeFeedback, botLocalFailure } from '../chat/botEmployeeFeedback'
import BotEmployeeMessage from './BotEmployeeMessage'

export default function BotContinueInteractive({ bot, runId, disabled, onChange }: { bot: Bot; runId: string; disabled: boolean; onChange: () => void }) {
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
    try { await continueInteractive(bot, runId, key.current); if (alive.current) { setAccepted(true); onChange() } }
    catch (e) { if (alive.current) setError(botLocalFailure(e).message) }
    finally { lock.current = false; if (alive.current) setPending(false) }
  }
  return <section aria-label={`Continue run ${runId}`} className="border-b border-border px-4 py-3 text-sm">
    <BotEmployeeMessage>{botEmployeeFeedback({ kind: 'continuation_authorization_required' })}</BotEmployeeMessage>
    <Button disabled={disabled || pending || accepted} onClick={() => { void resume() }}>{accepted ? 'Continuation requested' : pending ? 'Continuing…' : 'Continue interactive'}</Button>
    {error && <BotEmployeeMessage role="alert">{error}</BotEmployeeMessage>}
  </section>
}
