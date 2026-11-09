import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Bot, BotRun } from '@/api/bots'
import { cancelBotRun, canStopBotRun } from '@/api/botRunControls'

/** Key this control by Bot/scope/run identity so review never follows navigation. */
export default function BotStopRun({ bot, run, disabled = false, onChange }: { bot: Bot; run: BotRun; disabled?: boolean; onChange: () => void }) {
  const [review, setReview] = useState(false), [saving, setSaving] = useState(false)
  const [error, setError] = useState(''), [accepted, setAccepted] = useState<BotRun | null>(null)
  const pending = useRef(false), mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const eligible = run.bot_id === bot.id && canStopBotRun(run)
  const stop = async () => {
    if (!review || disabled || !eligible || pending.current || accepted) return
    pending.current = true; setSaving(true); setError('')
    try {
      const result = await cancelBotRun(bot, run.id)
      if (!mounted.current) return
      setAccepted(result); setReview(false); onChange()
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    } finally {
      pending.current = false
      if (mounted.current) setSaving(false)
    }
  }
  if (accepted) return <p role="status" className="text-sm text-muted-foreground">Stop request accepted for run {run.id} · {accepted.status}. Execution teardown may still be pending; this is not proof that external work has stopped.</p>
  if (!eligible && !review) return null
  return <div className="space-y-2">
    {!review ? <Button size="sm" variant="outline" disabled={disabled || !eligible} onClick={() => setReview(true)}>Stop run</Button> : <section aria-label={`Review stop run ${run.id}`} className="rounded border border-border p-3 space-y-2">
      <p>Stop only run <strong className="break-all">{run.id}</strong> of {bot.name} ({bot.scope})? Current status: {run.status}.</p>
      <p className="text-sm text-muted-foreground">This does not stop or pause the Bot, change its schedule, or stop other runs. Already committed external effects cannot be undone. Running execution may take time to tear down.</p>
      {!eligible && <p role="status">This run can no longer be stopped.</p>}
      <div className="flex gap-2"><Button size="sm" variant="destructive" disabled={disabled || saving || !eligible} onClick={stop}>{saving ? 'Requesting stop…' : 'Confirm stop run'}</Button><Button size="sm" variant="ghost" disabled={saving} onClick={() => { setReview(false); setError('') }}>Keep run</Button></div>
      {error && <p role="alert" className="text-destructive">{error}</p>}
    </section>}
  </div>
}
