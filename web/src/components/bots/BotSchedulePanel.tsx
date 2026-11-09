import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { getBotVersion, listBotGoals, type Bot, type BotGoal, type BotVersion } from '@/api/bots'
import { actOnTrigger, canEnableTrigger, HookStatus, isHook, saveTrigger, TriggerKind, type TriggerSpec, type TriggerProof } from '@/api/botTriggers'

type Props = { bot: Bot; version: BotVersion; readOnly: boolean; onChange: () => void }
const empty = (timezone: string): TriggerSpec => ({ id: '', kind: TriggerKind.Cron, cron: '', timezone, instructions: '', enabled: false, catch_up_once: false })
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block space-y-1 text-sm"><span className="font-medium">{label}</span>{children}</label> }

export default function BotSchedulePanel({ bot, version, readOnly, onChange }: Props) {
  const [snapshot, setSnapshot] = useState({ bot, version })
  const [draft, setDraft] = useState<TriggerSpec>(() => empty(version.persona.timezone || 'UTC'))
  const [editing, setEditing] = useState(false), [reviewed, setReviewed] = useState(false)
  const [reason, setReason] = useState(''), [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('')
  const [stale, setStale] = useState(false), [goals, setGoals] = useState<BotGoal[]>([])
  const [pending, setPending] = useState<{ trigger: TriggerSpec; action: 'enable' | 'disable' | 'remove' | 'dry-run' } | null>(null)
  const [proof, setProof] = useState({ event_id: '', timestamp: '', signature: '', payload: '' })
  const lock = useRef(false)
  useEffect(() => {
    if (bot.id === snapshot.bot.id && bot.revision < snapshot.bot.revision) return
    setSnapshot({ bot, version }); setStale(false); setPending(null); setReviewed(false)
  }, [bot, version])
  useEffect(() => {
    if (draft.kind !== TriggerKind.Deadline) return
    const controller = new AbortController()
    listBotGoals(bot.id, bot.scope, controller.signal).then(result => { if (!controller.signal.aborted) setGoals(result.goals || []) }).catch(e => { if (!controller.signal.aborted) setError(String(e)) })
    return () => controller.abort()
  }, [bot.id, bot.scope, draft.kind])
  const current = snapshot.bot
  const blocked = readOnly || busy || stale
  const change = (patch: Partial<TriggerSpec>) => { setDraft(d => ({ ...d, ...patch })); setReviewed(false); setPending(null) }
  const reset = () => { setDraft(empty(snapshot.version.persona.timezone || 'UTC')); setEditing(false); setReviewed(false) }
  const apply = async (fn: () => Promise<Bot>, message: string, after?: () => void) => {
    if (blocked || lock.current) return
    lock.current = true; setBusy(true); setError(''); setNotice('')
    try {
      const saved = await fn()
      // No optimistic approval/enabled state: load the exact immutable server version.
      if (saved) {
        setStale(true)
        setSnapshot(previous => ({ bot: saved, version: previous.version }))
        const savedVersion = await getBotVersion(saved.id, saved.version, saved.scope)
        setSnapshot({ bot: saved, version: savedVersion }); setStale(false)
      }
      setPending(null); setProof({ event_id: '', timestamp: '', signature: '', payload: '' }); setNotice(message); after?.(); onChange()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { lock.current = false; setBusy(false) }
  }
  const needsSchedule = draft.kind === TriggerKind.Cron || !!draft.poll
  const valid = !!reason.trim() && !!draft.instructions.trim()
    && (!needsSchedule || !!draft.cron?.trim() && !!draft.timezone?.trim())
    && (!isHook(draft) || !!draft.credential_ref?.trim())
    && (draft.kind !== TriggerKind.Deadline || !!draft.goal_id)
    && (draft.kind !== TriggerKind.Channel || !!draft.channel_binding?.id && !!draft.channel_binding.channel && !!draft.channel_binding.chat_id)
    && (!draft.poll || !!draft.poll.url && !!draft.poll.allowed_host)
  const signedProof = (): TriggerProof | undefined => pending && !pending.trigger.poll ? {
    bot_id: current.id, trigger_id: pending.trigger.id, version: snapshot.version.version,
    event_id: proof.event_id, timestamp: Number(proof.timestamp), signature: proof.signature, ...(proof.payload ? { payload: proof.payload } : {}),
  } : undefined
  return <section className="space-y-5 max-w-3xl">
    <header><h2 className="font-semibold">Schedule &amp; triggers</h2><p className="text-sm text-muted-foreground">Save disabled, review, then enable. Hook edits invalidate dry-run approval. Only credential references are used here, never webhook secrets.</p></header>
    <p className="text-sm text-muted-foreground">Next run: unavailable from the Bot API. A saved schedule is not a guarantee of execution; the Bot must be active and the trigger enabled.</p>
    {!snapshot.version.triggers?.length && <p>No scheduled triggers.</p>}
    <div className="space-y-3">{snapshot.version.triggers?.map(raw => { const t = raw as TriggerSpec; return <article key={t.id} className="rounded-lg border border-border p-4 space-y-2">
      <div className="flex flex-wrap justify-between gap-2"><h3 className="font-medium">{t.kind} · {t.enabled ? 'enabled' : 'disabled'}</h3><span className="text-xs text-muted-foreground">{t.id}</span></div>
      {t.cron && <p>{t.cron} · {t.timezone}</p>}<p className="text-sm whitespace-pre-wrap">{t.instructions}</p>
      <p className="text-sm">Catch up once: {t.catch_up_once ? 'yes' : 'no'}</p>
      {t.goal_id && <p className="text-sm">Linked goal: {t.goal_id} · deadline managed by the goal</p>}
      {isHook(t) && <p className="text-sm">Dry-run status: {t.hook_status || 'unavailable'} · Credential reference: {t.credential_ref || 'unavailable'}</p>}
      {t.poll && <p className="text-sm break-all">Poll: {t.poll.url} · allowed host: {t.poll.allowed_host}</p>}
      {t.channel_binding && <p className="text-sm">Binding: {t.channel_binding.id} · {t.channel_binding.channel} / {t.channel_binding.chat_id}{t.channel_binding.thread_id ? ` / ${t.channel_binding.thread_id}` : ''}</p>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" disabled={blocked || t.kind === 'manual'} onClick={() => { setDraft({ ...t }); setEditing(true); setReviewed(false); setPending(null) }}>Edit trigger</Button>
        <Button size="sm" variant="outline" disabled={blocked || !reason.trim() || editing || (!t.enabled && !canEnableTrigger(current, t))} onClick={() => setPending({ trigger: t, action: t.enabled ? 'disable' : 'enable' })}>{t.enabled ? 'Disable trigger' : 'Enable reviewed trigger'}</Button>
        {isHook(t) && <Button size="sm" variant="outline" disabled={blocked || editing || !reason.trim() || t.enabled || t.hook_status !== HookStatus.Disabled} onClick={() => { setPending({ trigger: t, action: 'dry-run' }); setProof({ event_id: '', timestamp: '', signature: '', payload: '' }) }}>Review dry-run</Button>}
        <Button size="sm" variant="outline" disabled={blocked || editing || !reason.trim()} onClick={() => setPending({ trigger: t, action: 'remove' })}>Remove trigger</Button>
      </div>
      {!t.enabled && isHook(t) && t.hook_status !== HookStatus.Passed && <p className="text-xs text-muted-foreground">Enabling requires a successful server-validated dry-run of this saved configuration.</p>}
    </article> })}</div>
    <Field label="Trigger change reason"><Input aria-label="Trigger change reason" disabled={blocked} value={reason} onChange={e => { setReason(e.target.value); setReviewed(false); setPending(null) }} placeholder="Why is this change needed?" /></Field>
    {pending && <div className="rounded-lg border border-border bg-muted/30 p-4 space-y-3" role="region" aria-label="Review trigger action">
      <h3 className="font-medium">Review {pending.action}</h3><p>{pending.trigger.kind} · {pending.trigger.id}</p><p>{pending.trigger.instructions}</p><p>Reason: {reason}</p>
      {pending.action === 'remove' && <p>This removes the trigger and its associated schedule. It does not delete the Bot or goal.</p>}
      {pending.action === 'enable' && <p>Enable this saved configuration for future events. Server validation is required.</p>}
      {pending.action === 'dry-run' && (pending.trigger.poll ? <p>The server will fetch the reviewed endpoint and validate the credential. This does not enable the hook.</p> : <div className="space-y-2">
        <p className="text-sm">Supply a signature generated externally for purpose “dry-run”, Bot {current.id}, trigger {pending.trigger.id}, version {snapshot.version.version}. The timestamp must be within the server’s five-minute window. Do not paste a signing secret.</p>
        <Field label="Proof event ID"><Input value={proof.event_id} onChange={e => setProof(p => ({ ...p, event_id: e.target.value }))} /></Field>
        <Field label="Proof timestamp (Unix seconds)"><Input inputMode="numeric" value={proof.timestamp} onChange={e => setProof(p => ({ ...p, timestamp: e.target.value }))} /></Field>
        <Field label="Proof signature"><Input value={proof.signature} onChange={e => setProof(p => ({ ...p, signature: e.target.value }))} /></Field>
        <Field label="Proof payload (base64, exact signed bytes)"><Textarea value={proof.payload} onChange={e => setProof(p => ({ ...p, payload: e.target.value }))} /></Field>
      </div>)}
      <div className="flex gap-2"><Button disabled={blocked || !reason.trim() || (pending.action === 'dry-run' && !pending.trigger.poll && (!proof.event_id.trim() || !proof.signature.trim() || !Number.isSafeInteger(Number(proof.timestamp)) || Number(proof.timestamp) <= 0))} onClick={() => void apply(() => actOnTrigger(current, pending.trigger, pending.action, reason, pending.action === 'dry-run' ? signedProof() : undefined), 'Trigger action confirmed by server.')}>Confirm {pending.action}</Button><Button variant="outline" disabled={busy} onClick={() => setPending(null)}>Cancel action</Button></div>
    </div>}
    <form className="rounded-lg border border-border p-4 space-y-4" onSubmit={e => { e.preventDefault(); if (!valid || editing && !reviewed) return; void apply(() => saveTrigger(current, { ...draft, id: draft.id || crypto.randomUUID() }, reason), 'Saved disabled. Review before enabling.', reset) }}>
      <h3 className="font-medium">{editing ? 'Edit saved trigger' : 'Add disabled schedule'}</h3>
      <fieldset disabled={blocked} className="space-y-3">
        <Field label="Trigger type"><select className="w-full rounded border border-border bg-background p-2" value={draft.kind} disabled={editing} onChange={e => { setDraft({ ...empty(snapshot.version.persona.timezone || 'UTC'), kind: e.target.value as TriggerSpec['kind'] }); setReviewed(false); setPending(null) }}><option value="cron">Cron schedule</option><option value="hook">Hook</option><option value="webhook">Signed webhook</option><option value="goal_deadline">Goal deadline</option><option value="channel">Channel binding</option></select></Field>
        {draft.kind === TriggerKind.Hook && <label className="flex gap-2 text-sm"><input type="checkbox" checked={!!draft.poll} onChange={e => change({ poll: e.target.checked ? { url: '', allowed_host: '' } : undefined })} />Poll an HTTPS endpoint</label>}
        {needsSchedule && <div className="grid gap-3 sm:grid-cols-2"><Field label="Cron expression"><Input aria-label="Cron expression" value={draft.cron || ''} onChange={e => change({ cron: e.target.value })} placeholder="0 9 * * *" /></Field><Field label="Schedule timezone"><Input aria-label="Schedule timezone" value={draft.timezone || ''} onChange={e => change({ timezone: e.target.value })} /></Field><p className="text-xs text-muted-foreground sm:col-span-2">Five fields: minute, hour, day, month, weekday. Use an IANA timezone such as Europe/Berlin. The server computes execution times; no client-side preview is estimated.</p></div>}
        {isHook(draft) && <Field label="Credential reference"><Input value={draft.credential_ref || ''} onChange={e => change({ credential_ref: e.target.value })} placeholder="Existing credential reference, not a secret" /></Field>}
        {draft.poll && <><Field label="Poll HTTPS URL"><Input value={draft.poll.url} onChange={e => change({ poll: { ...draft.poll!, url: e.target.value } })} /></Field><Field label="Allowed host"><Input value={draft.poll.allowed_host} onChange={e => change({ poll: { ...draft.poll!, allowed_host: e.target.value } })} /></Field><p className="text-xs text-muted-foreground">Exact lowercase host; no query, fragment, user info or non-443 port.</p></>}
        {draft.kind === TriggerKind.Webhook && <p className="text-sm text-muted-foreground">Signed events use the authenticated, scoped Bot events endpoint. The credential reference binds the signing credential; this form does not generate or reveal its secret.</p>}
        {draft.kind === TriggerKind.Deadline && <><Field label="Deadline goal"><select className="w-full rounded border border-border bg-background p-2" value={draft.goal_id || ''} onChange={e => change({ goal_id: e.target.value })}><option value="">Select an active goal with a deadline</option>{goals.filter(g => g.status === 'active' && g.due_at).map(g => <option key={g.id} value={g.id}>{g.title} — {g.due_at}</option>)}</select></Field><p className="text-xs text-muted-foreground">Uses the goal’s persisted due date, not a recurring cron. Only active goals with a saved deadline are eligible.</p></>}
        {draft.kind === TriggerKind.Channel && <div className="space-y-2"><p className="text-sm text-muted-foreground">Use an existing linked identity. A binding ID is not a new authorization grant; incoming events are checked by the server.</p>{(['id', 'channel', 'chat_id', 'thread_id'] as const).map(key => <Field key={key} label={{ id: 'Binding ID', channel: 'Channel', chat_id: 'Chat ID', thread_id: 'Thread ID (optional)' }[key]}><Input value={draft.channel_binding?.[key] || ''} onChange={e => change({ channel_binding: { id: '', channel: '', chat_id: '', ...draft.channel_binding, [key]: e.target.value } })} /></Field>)}</div>}
        <Field label="Schedule instructions"><Textarea aria-label="Schedule instructions" value={draft.instructions} onChange={e => change({ instructions: e.target.value })} placeholder="What should the Bot do when this trigger fires?" /></Field>
        {(needsSchedule || draft.kind === TriggerKind.Deadline) && <label className="flex gap-2 text-sm"><input type="checkbox" checked={draft.catch_up_once} onChange={e => change({ catch_up_once: e.target.checked })} />Catch up once after a missed occurrence</label>}
        {editing && <label className="flex gap-2 text-sm"><input type="checkbox" checked={reviewed} onChange={e => setReviewed(e.target.checked)} />I reviewed these changes. Save disabled and invalidate previous hook approval.</label>}
        <div className="flex gap-2"><Button disabled={!valid || editing && !reviewed}>{editing ? 'Save reviewed changes disabled' : 'Save disabled schedule'}</Button>{editing && <Button type="button" variant="outline" onClick={reset}>Cancel editing</Button>}</div>
      </fieldset>
    </form>
    {error && <p role="alert" className="text-destructive">{error}</p>}{stale && <p role="alert">Saved state could not be refreshed. Reload the Bot before another change.</p>}{notice && <p role="status">{notice}</p>}
  </section>
}
