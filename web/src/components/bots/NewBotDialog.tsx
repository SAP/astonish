import { useRef, useState } from 'react'
import { createBot } from '@/api/bots'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { botPath } from './botRoute'

export default function NewBotDialog({ onClose, onSources }: { onClose: () => void; onSources: () => void }) {
  const [name, setName] = useState(''), [busy, setBusy] = useState(false), [error, setError] = useState('')
  const request = useRef<{ name: string; key: string } | null>(null)
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose() }}><DialogContent><DialogTitle>New Bot</DialogTitle><DialogDescription>Start a private conversation. Execution and ongoing responsibilities require separate review.</DialogDescription><form className="space-y-3" onSubmit={async e => {
    e.preventDefault(); if (!name.trim() || busy) return
    setBusy(true); setError('')
    if (request.current?.name !== name.trim()) request.current = { name: name.trim(), key: crypto.randomUUID() }
    try {
      const bot = await createBot({ name: name.trim(), start_from_title: true, idempotency_key: request.current.key })
      window.dispatchEvent(new Event('astonish:bots-updated')); window.location.hash = botPath(bot.id, bot.scope); onClose()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
  }}><label>Name<Input autoFocus aria-label="Bot name" value={name} disabled={busy} onChange={e => setName(e.target.value)} /></label>{error && <p role="alert">{error}</p>}<Button disabled={!name.trim() || busy}>{busy ? 'Creating…' : 'Start conversation'}</Button><Button type="button" variant="ghost" disabled={busy} onClick={onSources}>Use a template or existing schedule</Button></form></DialogContent></Dialog>
}
