import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { requestBotRun, type Bot, type BotRun } from '@/api/bots'

/** Manual admission uses the existing scoped API, with stable retries after ambiguous failures. */
export default function BotRunNow({ bot, disabled, onRun }: { bot: Bot; disabled: boolean; onRun: (run: BotRun) => void }) {
  const [instructions, setInstructions] = useState(''), [busy, setBusy] = useState(false), [error, setError] = useState('')
  const request = useRef<{ text: string; version: number; key: string } | null>(null)
  return <form className="space-y-2" onSubmit={async event => {
    event.preventDefault()
    if (disabled || busy || !instructions.trim()) return
    if (!request.current || request.current.text !== instructions || request.current.version !== bot.version) request.current = { text: instructions, version: bot.version, key: crypto.randomUUID() }
    setBusy(true); setError('')
    try { const run = await requestBotRun(bot, instructions, request.current.key); setInstructions(''); request.current = null; onRun(run) }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }}>
    <Textarea aria-label="Run instructions" value={instructions} disabled={disabled || busy} onChange={event => setInstructions(event.target.value)} />
    <Button disabled={disabled || busy || !instructions.trim()}>Run now</Button>
    {error && <p role="alert">{error}</p>}
    <p className="text-xs text-muted-foreground">Use Stop run on a queued, running, or waiting-for-approval run to review cancellation of only that run.</p>
  </form>
}
