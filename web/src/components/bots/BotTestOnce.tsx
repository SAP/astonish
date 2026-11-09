import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { BotStatus, requestBotTestRun, type Bot, type BotRun } from '@/api/bots'

/** A reviewed one-shot admission, never a lifecycle transition. */
export default function BotTestOnce({ bot, disabled, onRun }: { bot: Bot; disabled: boolean; onRun: (run: BotRun) => void }) {
  const [open, setOpen] = useState(false), [instructions, setInstructions] = useState('')
  const [consentVersion, setConsentVersion] = useState<number | null>(null)
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const submitting = useRef(false)
  const request = useRef<{ text: string; version: number; key: string } | null>(null)
  if (bot.status !== BotStatus.Draft) return null
  const confirm = async () => {
    if (disabled || submitting.current || consentVersion !== bot.version || !instructions.trim()) return
    if (!request.current || request.current.text !== instructions || request.current.version !== bot.version) request.current = { text: instructions, version: bot.version, key: crypto.randomUUID() }
    submitting.current = true; setBusy(true); setError('')
    try {
      const run = await requestBotTestRun(bot, instructions, request.current.key)
      request.current = null; setInstructions(''); setConsentVersion(null); setOpen(false); onRun(run)
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { submitting.current = false; setBusy(false) }
  }
  return <div className="space-y-2">
    <Button size="sm" variant="outline" disabled={disabled || busy} onClick={() => { setOpen(true); setConsentVersion(null) }}>Test once</Button>
    {open && <section aria-label="Review test once" className="rounded border border-border p-3 space-y-2">
      <p>Review one test of version {bot.version}. This can execute tools under the displayed policy and may have external effects. The Bot stays draft; schedules stay disabled. This is not activation.</p>
      <Textarea aria-label="Test instructions" value={instructions} disabled={disabled || busy} onChange={event => { setInstructions(event.target.value); setConsentVersion(null) }} />
      <label className="flex gap-2 text-sm"><input type="checkbox" checked={consentVersion === bot.version} disabled={disabled || busy} onChange={event => setConsentVersion(event.target.checked ? bot.version : null)} />I authorize this one test under the displayed policy</label>
      <Button disabled={disabled || busy || consentVersion !== bot.version || !instructions.trim()} onClick={confirm}>Confirm test once</Button>
      <Button variant="ghost" disabled={busy} onClick={() => { setOpen(false); setConsentVersion(null) }}>Cancel test</Button>
      {error && <p role="alert" className="text-destructive">{error} Retrying unchanged instructions uses the same request identity.</p>}
    </section>}
  </div>
}
