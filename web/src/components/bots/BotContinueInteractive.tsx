import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Bot } from '@/api/bots'
import { continueInteractive } from '@/api/botReviews'

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
    catch (e) { if (alive.current) setError(e instanceof Error ? e.message : 'Continuation unavailable') }
    finally { lock.current = false; if (alive.current) setPending(false) }
  }
  return <section aria-label={`Continue run ${runId}`} className="border-b border-border px-4 py-3 text-sm">
    <p>This turn needs your authorization to continue. Your original message will not be sent again.</p>
    <Button disabled={disabled || pending || accepted} onClick={() => { void resume() }}>{accepted ? 'Continuation requested' : pending ? 'Continuing…' : 'Continue interactive'}</Button>
    {error && <p role="alert">{error}</p>}
  </section>
}
