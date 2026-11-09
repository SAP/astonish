import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BotAPIError, BotStatus, getBot, type Bot } from '@/api/bots'
import { getActiveTeam } from '@/api/teamContext'
import { AutonomyLevel, NotifyPolicy, configureBot, getBotConfigurationVersion, type AutonomyLevelValue, type NotifyPolicyValue, type BotConfiguration, type BotConfigurationRequest } from '@/api/botConfiguration'

type Props = { bot: Bot; readOnly?: boolean; onChange: () => void }
type Baseline = { bot: Bot; policy: BotConfiguration }
const lists = { allowed_tools: 'Allowed tools', denied_tools: 'Denied tools', allowed_paths: 'Allowed paths', allowed_hosts: 'Allowed hosts' } as const
const budgets = { max_runtime_seconds: 'Maximum runtime (seconds)', max_tool_calls: 'Maximum tool calls', max_tokens: 'Maximum tokens', max_runs_per_day: 'Maximum runs per day', max_model_cost_micros: 'Maximum model cost (micros)' } as const
const spend = { max_spend_per_run_minor: 'Spend per run (minor units)', max_spend_per_month_minor: 'Spend per month (minor units)' } as const
const field = 'grid gap-1 text-sm'
const select = 'rounded-md border border-input bg-background p-2 text-foreground'
function clean(policy: BotConfiguration): BotConfiguration {
  const autonomy = { ...policy.autonomy }
  for (const key of Object.keys(lists) as (keyof typeof lists)[]) autonomy[key] = (autonomy[key] || []).map(s => s.trim()).filter(Boolean)
  return { autonomy, budget: { ...policy.budget }, delivery: { ...policy.delivery, bindings: (policy.delivery.bindings || []).map(b => ({ ...b })) } }
}
function describe(policy: BotConfiguration): Record<string, string> {
  const result: Record<string, string> = { 'Autonomy level': policy.autonomy.level }
  for (const [key, label] of Object.entries(lists)) result[label] = (policy.autonomy[key as keyof typeof lists] || []).join(', ') || '(empty)'
  result['May contact people'] = policy.autonomy.can_contact_people ? 'Yes' : 'No'
  result['May publish'] = policy.autonomy.can_publish ? 'Yes' : 'No'
  result['Spend currency'] = policy.autonomy.spend_currency || '(none)'
  for (const [key, label] of Object.entries(spend)) result[label] = String(policy.autonomy[key as keyof typeof spend])
  for (const [key, label] of Object.entries(budgets)) result[label] = String(policy.budget[key as keyof typeof budgets] ?? 'Unlimited / unknown')
  result.Notifications = policy.delivery.notify
  result['Delivery preferences'] = policy.delivery.preferences || '(none)'
  result['Delivery destinations'] = (policy.delivery.bindings || []).map(b => `${b.channel}: ${b.chat_id}${b.thread_id ? ` / ${b.thread_id}` : ''} (linked identity ${b.id})`).join('; ') || '(none)'
  return result
}

/** Key by tenant, scope and bot, never by revision: polling must not erase edits. */
export default function BotConfigurationPanel(props: Props) {
  return <ConfigurationEditor key={`${getActiveTeam() || ''}/${props.bot.scope}/${props.bot.id}`} {...props} />
}
function ConfigurationEditor({ bot, readOnly = false, onChange }: Props) {
  const [baseline, setBaseline] = useState<Baseline>()
  const [draft, setDraft] = useState<BotConfiguration>()
  const [reason, setReason] = useState(''), [review, setReview] = useState<BotConfigurationRequest>()
  const [reviewed, setReviewed] = useState(false), [rights, setRights] = useState(false)
  const [error, setError] = useState(''), [message, setMessage] = useState('')
  const [busy, setBusy] = useState(true), [conflict, setConflict] = useState(false)
  const alive = useRef(true), initialBot = useRef(bot)
  const editableStatus = bot.status === BotStatus.Draft || bot.status === BotStatus.Paused || bot.status === BotStatus.Stopped
  const unavailable = readOnly || !editableStatus
  const resetReview = () => { setReview(undefined); setReviewed(false); setRights(false); setMessage('') }
  useEffect(() => {
    alive.current = true
    const controller = new AbortController()
    getBotConfigurationVersion(initialBot.current, controller.signal).then(policy => {
      if (!alive.current || controller.signal.aborted) return
      if (policy.bot_id !== initialBot.current.id || policy.version !== initialBot.current.version) throw new Error('Configuration version does not match the Bot. Reload the baseline.')
      setBaseline({ bot: initialBot.current, policy }); setDraft(clean(policy))
    }).catch(e => { if (!controller.signal.aborted) setError(e.message) }).finally(() => { if (!controller.signal.aborted) setBusy(false) })
    return () => { alive.current = false; controller.abort() }
  }, [])
  async function loadLatest() {
    setBusy(true); setError(''); resetReview()
    try {
      const latest = await getBot(bot.id, bot.scope)
      const policy = await getBotConfigurationVersion(latest)
      if (!alive.current) return
      if (policy.bot_id !== latest.id || policy.version !== latest.version) throw new Error('Configuration version mismatch; retry loading the baseline.')
      setBaseline({ bot: latest, policy }); setDraft(current => current || clean(policy)); setConflict(false)
    } catch (e) { if (alive.current) setError((e as Error).message) }
    finally { if (alive.current) setBusy(false) }
  }
  function change(next: BotConfiguration) { setDraft(next); resetReview() }
  async function save() {
    if (!review || !reviewed || !rights || unavailable || conflict || busy) return
    setBusy(true); setError('')
    try {
      const saved = await configureBot(bot.id, bot.scope, review)
      if (!alive.current) return
      setBaseline({ bot: saved, policy: review }); setDraft(clean(review)); resetReview(); setReason('')
      setMessage('Configuration saved. Activation and runs are separate actions.'); onChange()
    } catch (e) {
      if (!alive.current) return
      setError((e as Error).message); setReviewed(false); setRights(false)
      if (e instanceof BotAPIError && e.status === 409) { setConflict(true); setReview(undefined) }
    } finally { if (alive.current) setBusy(false) }
  }
  const before = baseline ? describe(baseline.policy) : {}
  const after = review ? describe(review) : {}
  const newer = baseline && (bot.revision !== baseline.bot.revision || bot.version !== baseline.bot.version)
  return <section aria-label="Runtime configuration" className="space-y-3 rounded-lg border border-border bg-card p-4 text-card-foreground">
    <h2 className="font-semibold">Autonomy, budgets & delivery</h2>
    <p className="text-sm text-muted-foreground">Policy changes require explicit review. Editing charter or prose does not grant rights. Saving does not activate this Bot or start a run.</p>
    {unavailable && <p>Configuration is read-only. Pause or stop an active Bot explicitly before changing runtime policy.</p>}
    {error && <p role="alert" className="text-destructive">{error}</p>}
    {message && <p role="status">{message}</p>}
    {!draft && !error && <p role="status">Loading runtime configuration…</p>}
    {baseline && <p className="text-sm">Baseline: revision {baseline.bot.revision} · version {baseline.bot.version}</p>}
    {(newer || conflict) && <p role="status">The Bot changed. Your edits are preserved; load the latest baseline and review all differences before retrying.</p>}
    <Button type="button" variant="outline" disabled={busy} onClick={loadLatest}>Load latest baseline, keep edits</Button>
    {draft && baseline && <form onSubmit={event => {
      event.preventDefault()
      if (unavailable || busy || conflict || !reason.trim()) return
      const numbers = [...Object.values(draft.budget), draft.autonomy.max_spend_per_run_minor, draft.autonomy.max_spend_per_month_minor].filter(value => value !== undefined)
      if (!numbers.every(value => Number.isSafeInteger(value) && value >= 0) || !(draft.budget.max_runtime_seconds > 0)) { setError('Use non-negative safe whole numbers for limits, and a positive runtime.'); return }
      if (!Object.values(AutonomyLevel).includes(draft.autonomy.level) || !Object.values(NotifyPolicy).includes(draft.delivery.notify)) { setError('Unsupported autonomy or notification policy. Choose a supported value.'); return }
      setError(''); setReviewed(false); setRights(false)
      setReview({ ...clean(draft), expected_revision: baseline.bot.revision, expected_version: baseline.bot.version, reviewed: true, reason: reason.trim() })
    }} className="space-y-4">
      <fieldset disabled={unavailable || busy} className="space-y-3">
        <legend className="font-medium">Autonomy and rights</legend>
        <label className={field}>Autonomy level<select className={select} value={draft.autonomy.level} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, level: e.target.value as AutonomyLevelValue } })}>
          {!Object.values(AutonomyLevel).includes(draft.autonomy.level) && <option value={draft.autonomy.level}>Unsupported: {draft.autonomy.level}</option>}
          <option value={AutonomyLevel.Ask}>Ask</option><option value={AutonomyLevel.Supervised}>Supervised</option><option value={AutonomyLevel.Reversible}>Autonomous — reversible</option><option value={AutonomyLevel.Scoped}>Autonomous — scoped</option>
        </select></label>
        <p className="text-sm text-muted-foreground">Empty allowlists use runtime defaults, not a promise of deny-all. Denied tools take precedence; server policy remains authoritative.</p>
        <div className="grid gap-3 sm:grid-cols-2">{(Object.keys(lists) as (keyof typeof lists)[]).map(key => <label className={field} key={key}>{lists[key]} (one per line)<Textarea rows={3} maxLength={16384} value={(draft.autonomy[key] || []).join('\n')} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, [key]: e.target.value.split('\n') } })} /></label>)}</div>
        <label className="flex gap-2"><input type="checkbox" checked={draft.autonomy.can_contact_people} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, can_contact_people: e.target.checked } })} />May contact people</label>
        <label className="flex gap-2"><input type="checkbox" checked={draft.autonomy.can_publish} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, can_publish: e.target.checked } })} />May publish</label>
        <label className={field}>Spend currency<Input maxLength={3} pattern="[A-Z]{3}" placeholder="USD" required={draft.autonomy.max_spend_per_run_minor > 0 || draft.autonomy.max_spend_per_month_minor > 0} value={draft.autonomy.spend_currency || ''} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, spend_currency: e.target.value.toUpperCase() } })} /></label>
        <div className="grid gap-3 sm:grid-cols-2">{(Object.keys(spend) as (keyof typeof spend)[]).map(key => <label className={field} key={key}>{spend[key]}<Input type="number" min={0} max={Number.MAX_SAFE_INTEGER} step={1} required value={draft.autonomy[key]} onChange={e => change({ ...draft, autonomy: { ...draft.autonomy, [key]: e.target.value === '' ? NaN : Number(e.target.value) } })} /></label>)}</div>
        <p className="text-sm text-muted-foreground">Purchase caps use currency minor units; zero does not grant spending. They are separate from model cost. Unsupported capabilities fail closed on the server.</p>
        <h3 className="font-medium">Budgets</h3>
        <p className="text-sm text-muted-foreground">Blank optional budgets mean unlimited / unknown; zero is an explicit limit. Model-cost caps cannot guarantee a money ceiling when model pricing or enforcement is unsupported.</p>
        <div className="grid gap-3 sm:grid-cols-2">{(Object.keys(budgets) as (keyof typeof budgets)[]).map(key => <label className={field} key={key}>{budgets[key]}<Input type="number" min={key === 'max_runtime_seconds' ? 1 : 0} max={Number.MAX_SAFE_INTEGER} step={1} required={key === 'max_runtime_seconds'} value={draft.budget[key] ?? ''} onChange={e => change({ ...draft, budget: { ...draft.budget, [key]: e.target.value === '' ? undefined : Number(e.target.value) } })} /></label>)}</div>
        <h3 className="font-medium">Delivery</h3>
        <label className={field}>Notifications<select className={select} value={draft.delivery.notify} onChange={e => change({ ...draft, delivery: { ...draft.delivery, notify: e.target.value as NotifyPolicyValue } })}>
          {!Object.values(NotifyPolicy).includes(draft.delivery.notify) && <option value={draft.delivery.notify}>Unsupported: {draft.delivery.notify}</option>}
          <option value={NotifyPolicy.Never}>Never</option><option value={NotifyPolicy.Change}>On change</option><option value={NotifyPolicy.Decision}>On decision needed</option><option value={NotifyPolicy.Always}>Always</option>
        </select></label>
        <label className={field}>Delivery preferences<Textarea maxLength={8192} value={draft.delivery.preferences} onChange={e => change({ ...draft, delivery: { ...draft.delivery, preferences: e.target.value } })} /></label>
        <p className="text-sm text-muted-foreground">Destinations must use an existing server-verified linked identity. Entering an ID is not a grant; unavailable installation or room proofs are rejected.</p>
        {!(draft.delivery.bindings || []).length && <p>No external delivery destinations.</p>}
        {(draft.delivery.bindings || []).map((binding, index) => <fieldset key={index} className="space-y-2 rounded border border-border p-3"><legend>Destination {index + 1}</legend>
          {(['id', 'channel', 'chat_id', 'thread_id'] as const).map(key => <label className={field} key={key}>{({ id: 'Linked identity ID', channel: 'Channel', chat_id: 'Chat ID', thread_id: 'Thread ID (optional)' })[key]}<Input maxLength={512} required={key !== 'thread_id'} value={binding[key] || ''} onChange={e => change({ ...draft, delivery: { ...draft.delivery, bindings: draft.delivery.bindings!.map((b, i) => i === index ? { ...b, [key]: e.target.value } : b) } })} /></label>)}
          <Button type="button" variant="outline" onClick={() => change({ ...draft, delivery: { ...draft.delivery, bindings: draft.delivery.bindings!.filter((_, i) => i !== index) } })}>Remove destination {index + 1}</Button>
        </fieldset>)}
        <Button type="button" variant="outline" disabled={(draft.delivery.bindings || []).length >= 20} onClick={() => change({ ...draft, delivery: { ...draft.delivery, bindings: [...(draft.delivery.bindings || []), { id: '', channel: '', chat_id: '' }] } })}>Add delivery destination</Button>
        <label className={field}>Review reason<Textarea required maxLength={2048} value={reason} onChange={e => { setReason(e.target.value); resetReview() }} /></label>
        <Button type="submit" disabled={!reason.trim() || conflict}>Review configuration</Button>
      </fieldset>
    </form>}
    {review && <section aria-label="Review configuration changes" className="space-y-3 border-t border-border pt-3">
      <h3 className="font-medium">Review exact configuration</h3>
      <p>Revision {review.expected_revision} · version {review.expected_version} · Reason: {review.reason}</p>
      <dl className="space-y-2 text-sm">{Object.entries(after).map(([label, value]) => <div key={label}><dt className="font-medium">{label}</dt><dd className="break-words">{before[label] !== value ? <><span className="text-muted-foreground">{before[label]}</span> → {value}</> : `${value} (unchanged)`}</dd></div>)}</dl>
      <label className="flex gap-2"><input type="checkbox" disabled={unavailable || busy} checked={reviewed} onChange={e => setReviewed(e.target.checked)} />I reviewed this exact configuration and reason</label>
      <label className="flex gap-2"><input type="checkbox" disabled={unavailable || busy} checked={rights} onChange={e => setRights(e.target.checked)} />I explicitly authorize the rights, spending and delivery destinations shown above</label>
      <Button type="button" disabled={unavailable || busy || conflict || !reviewed || !rights} onClick={save}>{busy ? 'Saving configuration…' : 'Save reviewed configuration'}</Button>
      <Button type="button" variant="ghost" disabled={busy} onClick={resetReview}>Back to editing</Button>
    </section>}
  </section>
}
