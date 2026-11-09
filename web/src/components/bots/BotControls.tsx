import { useState } from 'react'
import BotDeleteDialog from './BotDeleteDialog'
import { useBotClock } from './useBotClock'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BotStatus, botLifecycle, type Bot, type BotApproval, type BotVersion, decideBotApproval, resumeBotRun } from '@/api/bots'

export function BotLifecycleControls({ bot, onChange, readOnly }: { bot: Bot; onChange: () => void; readOnly: boolean }) {
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [action, setAction] = useState<Exclude<Parameters<typeof botLifecycle>[1], 'delete'> | null>(null), [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const apply = async () => {
    if (!action || busy || !reason.trim()) return
    setBusy(true); setError('')
    try { await botLifecycle(bot, action, reason); setAction(null); setReason(''); onChange() } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
  }
  if (readOnly) return <p className="text-xs text-muted-foreground">Read-only team workspace. Owner controls execution.</p>
  return <div className="space-y-2">
    <div className="flex flex-wrap gap-2">
      {bot.status !== BotStatus.Deleted && <>
        {bot.status !== BotStatus.Active && <Button size="sm" onClick={() => setAction('activate')}>Review activation</Button>}
        {bot.status === BotStatus.Active && <Button size="sm" variant="outline" onClick={() => setAction('pause')}>Pause</Button>}
        <Button size="sm" variant="outline" onClick={() => setAction('stop')}>Stop</Button>
      </>}
      <Button size="sm" variant="outline" onClick={() => { setAction(null); setDeleteOpen(true) }}>Delete Bot</Button>
      {bot.status === BotStatus.Deleted && !bot.permanent_delete && bot.restore_until && <Button size="sm" onClick={() => setAction('restore')}>Restore retained Bot</Button>}
    </div>
    {deleteOpen && <BotDeleteDialog key={`${bot.scope}/${bot.id}`} bot={bot} onClose={() => setDeleteOpen(false)} />}
    {action && <div className="rounded border border-border p-3 space-y-2"><p>Confirm {action} of revision {bot.revision}. {action === 'activate' ? 'This enables execution under the policy displayed above.' : action === 'restore' ? 'Restore does not activate the Bot.' : 'Already committed external effects cannot be undone.'}</p><Input aria-label="Lifecycle reason" placeholder="Reason for this action" value={reason} onChange={e => setReason(e.target.value)} /><Button disabled={busy || !reason.trim()} onClick={apply}>Confirm {action}</Button><Button variant="ghost" disabled={busy} onClick={() => setAction(null)}>Cancel</Button></div>}
    {error && <p role="alert" className="text-destructive">{error} Refresh to review the latest revision before retrying.</p>}
  </div>
}
export function BotPolicySummary({ version }: { version: BotVersion }) {
  return <p className="text-xs text-muted-foreground">{version.autonomy.level} · {version.persona.timezone || 'UTC'} · contact {version.autonomy.can_contact_people ? 'allowed by policy' : 'disabled'} · spend cap {version.autonomy.max_spend_per_run_minor} minor units/run · {version.budget.max_runtime_seconds}s/run · notifications {version.delivery.notify}</p>
}
export function BotDecisionInbox({ bot, approvals, onChange, readOnly }: { bot: Bot; approvals: BotApproval[]; onChange: () => void; readOnly: boolean }) {
  const [busy, setBusy] = useState(''), [error, setError] = useState('')
  const now = useBotClock()
  const act = async (approval: BotApproval, status: 'approved' | 'denied', answer?: string) => {
    if (busy) return
    setBusy(approval.id); setError('')
    try { await decideBotApproval(bot, approval, status, answer); onChange() } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy('') }
  }
  return <section aria-label="Decision inbox" className="border-b border-border p-3 space-y-2"><h2 className="font-semibold">Decision inbox</h2>
    {approvals.length === 0 && <p className="text-sm text-muted-foreground">No decisions waiting.</p>}
    {approvals.map(a => <div key={a.id} className="rounded border border-border p-3 space-y-2 text-sm"><p>{a.interaction?.payload.prompt || 'Permission to perform this exact action'}</p><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify(a.action, null, 2)}</pre><p>{a.status} · version {a.version} · expires {a.expires_at}</p>
      {a.status === 'pending' && <div className="flex gap-2 flex-wrap">{a.interaction ? (a.interaction.payload.kind === 'yesno' ? [{ id: 'yes', label: 'Yes' }, { id: 'no', label: 'No' }] : a.interaction.payload.options || []).map(o => <Button key={o.id} size="sm" disabled={readOnly || !!busy || Date.parse(a.expires_at) <= now} onClick={() => act(a, 'approved', o.id)}>{o.label}</Button>) : <Button size="sm" disabled={readOnly || !!busy || Date.parse(a.expires_at) <= now} onClick={() => act(a, 'approved')}>Approve exact action</Button>}<Button size="sm" variant="outline" disabled={readOnly || !!busy} onClick={() => act(a, 'denied')}>Deny</Button></div>}
      {a.status === 'approved' && <Button size="sm" disabled={readOnly || !!busy} onClick={async () => { setBusy(a.id); try { await resumeBotRun(bot, a); onChange() } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy('') } }}>Resume approved run</Button>}
    </div>)}{error && <p role="alert" className="text-destructive">{error}</p>}
  </section>
}

export default function ReviewedText({ label, initial = '', onSave, disabled = false }: { label: string; initial?: string; onSave: (value: string, reason: string) => Promise<unknown>; disabled?: boolean }) {
  const [value, setValue] = useState(initial), [reason, setReason] = useState(''), [reviewed, setReviewed] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState('')
  return <form className="space-y-2" onSubmit={async e => { e.preventDefault(); if (!reviewed || busy) return; setBusy(true); setError(''); try { await onSave(value, reason); setReviewed(false) } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) } }}>
    <label>{label}<Textarea aria-label={label} value={value} disabled={disabled || busy} onChange={e => { setValue(e.target.value); setReviewed(false) }} /></label>
    <Input aria-label={`${label} reason`} placeholder="Review reason" value={reason} disabled={disabled || busy} onChange={e => setReason(e.target.value)} />
    <label className="flex gap-2 text-sm"><input type="checkbox" checked={reviewed} disabled={disabled || busy} onChange={e => setReviewed(e.target.checked)} />I reviewed this change.</label>
    <Button disabled={disabled || busy || !reviewed || !value.trim() || !reason.trim()}>Save {label.toLowerCase()}</Button>{error && <p role="alert" className="text-destructive">{error}</p>}
  </form>
}
