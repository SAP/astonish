import { useEffect, useRef, useState } from 'react'
import { useBotClock } from './useBotClock'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BotAPIError, listBotGoals, type Bot, type BotGoal, type BotEvidence } from '@/api/bots'
import { createGoal, editGoal, confirmGoal, listTrackedItems, saveTrackedItem, GoalStatus, TrackedStatus, type TrackedStatusValue, type TrackedItem, type OwnerConfirmation } from '@/api/botGoals'

export interface BotGoalsPanelProps { bot: Bot; goals: BotGoal[]; readOnly: boolean; onChange: () => void }
const emptyGoal = { title: '', description: '', completion_criteria: '', due: '' }
const emptyItem = { title: '', status: TrackedStatus.Open as TrackedStatusValue, reason: '', due: '' }
const localDate = (iso?: string) => {
  if (!iso) return ''
  const date = new Date(iso)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
const dueDate = (value: string) => value ? new Date(value).toISOString() : null
function Evidence({ evidence }: { evidence: BotEvidence[] | null }) {
  return <ul className="space-y-2 text-sm">{evidence?.map((e, i) => <li key={i} className="rounded border border-border p-2"><strong>{e.kind === 'owner_confirmation' ? 'Owner confirmation' : e.kind === 'observation' ? 'Worker observation' : 'Inference (not completion proof)'}</strong><p>{e.summary}</p><p>Source: {e.source}</p><p>Observed: {new Date(e.observed_at).toLocaleString()} · Actor: {e.actor_id}</p></li>)}</ul>
}

export default function BotGoalsPanel(props: BotGoalsPanelProps) {
  return <GoalsEditor key={`${props.bot.scope}:${props.bot.id}`} {...props} />
}
function GoalsEditor({ bot, goals, readOnly, onChange }: BotGoalsPanelProps) {
  const [selected, setSelected] = useState<BotGoal | null>(null)
  const [draft, setDraft] = useState(emptyGoal)
  const [items, setItems] = useState<TrackedItem[]>([])
  const [itemID, setItemID] = useState('')
  const [item, setItem] = useState(emptyItem)
  const [proof, setProof] = useState<OwnerConfirmation>({ summary: '', source: '', observed_at: '' })
  const [reviewed, setReviewed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [stale, setStale] = useState(false)
  const [reload, setReload] = useState(0)
  const lock = useRef(false)
  const selection = useRef(0)
  const disabled = readOnly || busy || stale
  const completed = selected?.status === GoalStatus.Completed
  const now = useBotClock()
  const proofValid = !!proof.summary.trim() && !!proof.source.trim() && !!proof.observed_at && Number.isFinite(Date.parse(proof.observed_at)) && Date.parse(proof.observed_at) <= now
  const needsReason = item.status === TrackedStatus.Blocked || item.status === TrackedStatus.Cancelled
  const hasOpenItems = items.some(i => i.status === TrackedStatus.Open || i.status === TrackedStatus.Blocked)
  useEffect(() => {
    if (!selected) return
    const controller = new AbortController()
    setLoading(true); setItems([])
    listTrackedItems(bot, selected.id, controller.signal).then(result => {
      if (!controller.signal.aborted) setItems(result.items)
    }).catch(e => { if (!controller.signal.aborted) { setError(e instanceof Error ? e.message : String(e)); setStale(true) } }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [bot.id, bot.scope, selected?.id, reload]) // Revision reloads are explicit; unsaved drafts never follow polling.
  const clearProof = () => { setProof({ summary: '', source: '', observed_at: '' }); setReviewed(false) }
  const choose = (goal: BotGoal | null) => {
    selection.current++; setSelected(goal); setDraft(goal ? { title: goal.title, description: goal.description, completion_criteria: goal.completion_criteria, due: localDate(goal.due_at) } : emptyGoal)
    setItem(emptyItem); setItemID(''); clearProof(); setError(''); setNotice(''); setStale(false); setReload(n => n + 1)
  }
  const refresh = async () => {
    const generation = selection.current
    const result = await listBotGoals(bot.id, bot.scope)
    if (generation !== selection.current) return
    if (selected) {
      const latest = result.goals.find(g => g.id === selected.id)
      if (!latest) throw new Error('This goal is no longer available.')
      setSelected(latest)
    }
    setReload(n => n + 1); setStale(false); setReviewed(false); onChange()
  }
  const action = async (fn: () => Promise<unknown>, success: string, after?: () => void) => {
    if (disabled || lock.current) return
    lock.current = true; setBusy(true); setError(''); setNotice('')
    let saved = false
    try { await fn(); saved = true; await refresh(); after?.(); setNotice(success) }
    catch (e) {
      setError(saved ? `Saved, but current state could not be refreshed: ${e instanceof Error ? e.message : String(e)}` : e instanceof Error ? e.message : String(e))
      if (saved || e instanceof BotAPIError && e.status === 409) { setStale(true); setReviewed(false) }
    } finally { lock.current = false; setBusy(false) }
  }
  const updateProof = (field: keyof OwnerConfirmation, value: string) => { setProof(p => ({ ...p, [field]: value })); setReviewed(false) }
  return <section className="space-y-4">
    <h2 className="font-semibold">Goals and tracked commitments</h2>
    <p className="text-sm text-muted-foreground">Durable outcomes and responsibilities across runs. These are not a run’s temporary to-do list.</p>
    {readOnly && <p>Read-only: you can review goals and evidence, but cannot make changes.</p>}
    {!goals.length && <p>No goals yet. Add an observable outcome below.</p>}
    {goals.map(goal => <article key={goal.id} className="space-y-2 rounded border border-border p-3">
      <h3 className="font-medium">{goal.title} · {goal.status}</h3><p>{goal.description}</p><p>Completion criteria: {goal.completion_criteria}</p>
      <p>Due: {goal.due_at ? new Date(goal.due_at).toLocaleString() : 'No deadline'}</p><Evidence evidence={goal.evidence} />
      <Button variant="outline" disabled={busy} onClick={() => choose(goal)}>Review goal: {goal.title}</Button>
    </article>)}
    {selected && <Button variant="outline" disabled={busy} onClick={() => choose(null)}>Add another goal</Button>}
    {error && <p role="alert" className="text-destructive">{error}</p>}{notice && <p role="status">{notice}</p>}
    {stale && <div className="space-y-2"><p>Your draft is preserved. Reload the current goal and commitments, compare them with your draft, then review again before retrying.</p><Button disabled={busy} onClick={async () => { setBusy(true); try { await refresh(); setError(''); setNotice('Current state loaded. Review the latest criteria and commitments before saving your preserved draft.') } catch (e) { setError(String(e)) } finally { setBusy(false) } }}>Reload current state</Button></div>}
    {selected && <div className="rounded border border-border p-3"><h3>Current saved goal · revision {selected.revision}</h3><p>{selected.title}</p><p>{selected.description}</p><p>Criteria: {selected.completion_criteria}</p><p>Due: {selected.due_at ? new Date(selected.due_at).toLocaleString() : 'No deadline'}</p><Evidence evidence={selected.evidence} /></div>}
    <form onSubmit={e => { e.preventDefault(); if (!draft.title.trim() || !draft.completion_criteria.trim() || completed) return; const fields = { title: draft.title, description: draft.description, completion_criteria: draft.completion_criteria, due_at: dueDate(draft.due) }; void action(() => selected ? editGoal(bot, selected.id, { ...fields, expected_revision: selected.revision }) : createGoal(bot, fields), 'Goal saved.', () => { if (!selected) setDraft(emptyGoal) }) }}>
      <fieldset disabled={disabled || completed} className="space-y-2"><legend className="font-medium">{selected ? 'Edit goal' : 'Add a goal'}</legend>
        <label className="block">Goal title<Input value={draft.title} onChange={e => setDraft({ ...draft, title: e.target.value })} /></label>
        <label className="block">Goal description<Textarea value={draft.description} onChange={e => setDraft({ ...draft, description: e.target.value })} /></label>
        <label className="block">Completion criteria<Textarea value={draft.completion_criteria} onChange={e => setDraft({ ...draft, completion_criteria: e.target.value })} /></label>
        <label className="block">Goal due time (local, optional)<Input type="datetime-local" value={draft.due} onChange={e => setDraft({ ...draft, due: e.target.value })} /></label>
        <p className="text-sm text-muted-foreground">Clear the due time to remove the deadline. Times use your browser’s local timezone.</p>
        <Button disabled={!draft.title.trim() || !draft.completion_criteria.trim()}>{busy ? 'Saving…' : 'Save goal'}</Button>
      </fieldset>
    </form>
    {selected && <div className="space-y-4">
      <h3 className="font-medium">Tracked commitments for {selected.title}</h3>
      {loading ? <p role="status">Loading commitments…</p> : !items.length && !stale && <p>No tracked commitments.</p>}
      {items.map(i => <article key={i.id} className="space-y-2 rounded border border-border p-3"><h4>{i.title} · {i.status}</h4>{i.reason && <p>Reason: {i.reason}</p>}<p>Due: {i.due_at ? new Date(i.due_at).toLocaleString() : 'No deadline'}</p><Evidence evidence={i.evidence} /><Button disabled={disabled || completed || loading} variant="outline" onClick={() => { setItemID(i.id); setItem({ title: i.title, status: i.status, reason: i.reason ?? '', due: localDate(i.due_at) }); clearProof() }}>Edit commitment: {i.title}</Button></article>)}
      {!completed && <>
        <fieldset className="space-y-2" disabled={disabled || loading}><legend className="font-medium">{itemID ? 'Edit tracked commitment' : 'Add tracked commitment'}</legend>
          <label className="block">Commitment title<Input value={item.title} onChange={e => { setItem({ ...item, title: e.target.value }); setReviewed(false) }} /></label>
          <label className="block">Commitment status<select className="block rounded border border-border bg-background p-2" value={item.status} onChange={e => { setItem({ ...item, status: e.target.value as TrackedStatusValue }); setReviewed(false) }}>{Object.values(TrackedStatus).map(status => <option key={status} value={status}>{status}</option>)}</select></label>
          <label className="block">Commitment reason{needsReason ? ' (required)' : ''}<Textarea value={item.reason} onChange={e => { setItem({ ...item, reason: e.target.value }); setReviewed(false) }} /></label>
          <label className="block">Commitment due time (local, optional)<Input type="datetime-local" value={item.due} onChange={e => { setItem({ ...item, due: e.target.value }); setReviewed(false) }} /></label>
          <p className="text-sm text-muted-foreground">Blocked and cancelled commitments require a reason. Saving replaces the commitment’s evidence; completed status requires fresh owner confirmation below. Review the existing evidence above before changing it.</p>
          <Button disabled={!item.title.trim() || needsReason && !item.reason.trim() || item.status === TrackedStatus.Completed && (!proofValid || !reviewed)} onClick={() => { const id = itemID || crypto.randomUUID(); setItemID(id); void action(() => saveTrackedItem(bot, selected.id, id, { expected_goal_revision: selected.revision, title: item.title, status: item.status, reason: item.reason, due_at: dueDate(item.due), evidence: item.status === TrackedStatus.Completed ? [proof] : [] }), 'Commitment saved.', () => { setItem(emptyItem); setItemID(''); clearProof() }) }}>Save commitment</Button>
          {itemID && <Button variant="outline" onClick={() => { setItemID(''); setItem(emptyItem); clearProof() }}>Cancel commitment edit</Button>}
        </fieldset>
        <fieldset disabled={disabled || loading} className="space-y-2"><legend className="font-medium">Owner completion evidence</legend>
          <p>This records your confirmation, not an independent worker observation. Review the saved goal criteria and commitment evidence first.</p>
          <label className="block">Completion summary<Textarea value={proof.summary} onChange={e => updateProof('summary', e.target.value)} /></label>
          <label className="block">Evidence source<Input value={proof.source} onChange={e => updateProof('source', e.target.value)} /></label>
          <label className="block">Evidence observed time (local)<Input type="datetime-local" value={localDate(proof.observed_at)} onChange={e => updateProof('observed_at', dueDate(e.target.value) ?? '')} /></label>
          <label className="flex gap-2"><input type="checkbox" checked={reviewed} onChange={e => setReviewed(e.target.checked)} />I reviewed the saved criteria, commitments and evidence for this confirmation.</label>
          {hasOpenItems && <p>Complete or cancel every open or blocked commitment before completing the goal.</p>}
          <Button disabled={!proofValid || !reviewed || hasOpenItems || !!itemID} onClick={() => void action(() => confirmGoal(bot, selected.id, { expected_revision: selected.revision, evidence: proof }), 'Goal completed with owner confirmation.', clearProof)}>Confirm completion: {selected.title}</Button>
        </fieldset>
      </>}
      {completed && <p>This goal is completed. Its fields and commitments are read-only.</p>}
    </div>}
  </section>
}
