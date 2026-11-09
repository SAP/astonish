import { useCallback, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { createBot, listBotTemplates, getBotTemplate, type BotTemplate, type BotCarryover, type BotPersona } from '@/api/bots'
import { proposeSourceIntake, decideSourceIntake, type SourceIntakeProposal } from '@/api/botReviews'
import { BotIntakeWriter, IntakeMode, currentIntakeNamespace, type BotIntakeContent } from '@/api/botIntake'
import { getActiveTeam } from '@/api/teamContext'
import type { BotScope } from '@/api/bots'
import BotCarryoverFields from './BotCarryoverFields'
import { BotAPIError } from '@/api/bots'
import { type BotAdaptiveReview } from '@/api/botAdaptive'
import BotAdaptiveConversion from './BotAdaptiveConversion'
import { botPath } from './botRoute'

const Step = { Edit: 'edit', Review: 'review' } as const
const emptyCarryover = (): BotCarryover => ({ summary: '', memories: [], goals: [], artifacts: [] })
const emptyPersona = (): BotPersona => ({ language: '', timezone: 'UTC', soul: '', identity: '', user: '' })
export default function BotDraftDialog({ open, onClose, sourceSession: initialSource, resumeId, scope = 'personal', inline = false }: { open: boolean; onClose: () => void; sourceSession?: string | null; resumeId?: string; scope?: BotScope; inline?: boolean }) {
  const [sourceSession, setSourceSession] = useState(initialSource)
  const [resumeMode, setResumeMode] = useState<BotIntakeContent['mode']>(IntakeMode.Blank)
  const [adaptiveSource, setAdaptiveSource] = useState('')
  const [adaptiveReview, setAdaptiveReview] = useState<BotAdaptiveReview | null>(null)
  const adaptive = resumeMode === IntakeMode.Adaptive
  const [templateBlocked, setTemplateBlocked] = useState(false)
  const restoredSource = useRef<Pick<BotIntakeContent, 'source_id' | 'template_revision'>>({})
  const writer = useRef<BotIntakeWriter | null>(null)
  const [restoring, setRestoring] = useState(!!resumeId)
  const [conflict, setConflict] = useState<BotIntakeContent | null>(null)
  const [created, setCreated] = useState<{ id: string; scope: BotScope } | null>(null)
  const [savedSnapshot, setSavedSnapshot] = useState('')
  const team = getActiveTeam()
  const ensureWriter = async () => {
    if (!writer.current) writer.current = new BotIntakeWriter(resumeId || crypto.randomUUID(), scope, await currentIntakeNamespace(scope, team), team)
    return writer.current
  }
  const [sourceProposal, setSourceProposal] = useState<SourceIntakeProposal>()
  const proposalAttempt = useRef<{ snapshot: string; proposal: SourceIntakeProposal } | null>(null)
  const [name, setName] = useState(''), [charter, setCharter] = useState('')
  const [carryover, setCarryover] = useState<BotCarryover>(emptyCarryover)
  const [reviewed, setReviewed] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState('')
  const [step, setStep] = useState<typeof Step[keyof typeof Step]>(Step.Edit)
  const [templates, setTemplates] = useState<BotTemplate[] | null>(null), [selected, setSelected] = useState<BotTemplate | null>(null)
  const [loading, setLoading] = useState(false)
  const lookup = useRef<AbortController | null>(null)
  // Keep identical retries bound to one request; edited payloads are new requests.
  const attempt = useRef<{ payload: string; key: string } | null>(null)
  const [persona, setPersona] = useState<BotPersona>(emptyPersona)
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const dirty = adaptive || !!name || !!charter || !!selected || JSON.stringify(carryover) !== JSON.stringify(emptyCarryover())
    || JSON.stringify(persona) !== JSON.stringify(emptyPersona())
  const reset = useCallback(() => {
    setName(''); setCharter(''); setCarryover(emptyCarryover()); setSelected(null); setPersona(emptyPersona())
    setTemplates(null); setLoading(false); setError(''); setReviewed(false); setStep(Step.Edit); setConfirmDiscard(false); attempt.current = null
    setResumeMode(IntakeMode.Blank); setAdaptiveSource(''); setAdaptiveReview(null); setTemplateBlocked(false); restoredSource.current = {}
  }, [])
  const content: BotIntakeContent = { mode: selected ? IntakeMode.Template : sourceSession ? IntakeMode.Chat : resumeMode,
    name, charter: selected?.definition.charter ?? charter, language: persona.language, timezone: persona.timezone,
    identity: persona.identity, working_style: persona.soul, owner_preferences: persona.user, carryover,
    ...(adaptive ? (adaptiveSource.trim() ? { source_id: adaptiveSource.trim() } : {}) : selected ? { source_id: selected.id, template_revision: selected.revision } : sourceSession ? { source_id: sourceSession } : restoredSource.current) }
  const snapshot = JSON.stringify(content)
  const applyContent = async (c: BotIntakeContent) => {
    setResumeMode(c.mode); setTemplateBlocked(c.mode === IntakeMode.Template)
    setAdaptiveSource(c.mode === IntakeMode.Adaptive ? c.source_id || '' : ''); setAdaptiveReview(null)
    restoredSource.current = { ...(c.source_id ? { source_id: c.source_id } : {}), ...(c.template_revision !== undefined ? { template_revision: c.template_revision } : {}) }
    // Persisted Go slices may decode as null; carryover editors require arrays.
    setName(c.name); setCharter(c.charter); setCarryover({ summary: c.carryover?.summary || '', memories: c.carryover?.memories || [], goals: c.carryover?.goals || [], artifacts: c.carryover?.artifacts || [] })
    setPersona({ language: c.language, timezone: c.timezone, identity: c.identity, soul: c.working_style, user: c.owner_preferences })
    setSourceSession(c.mode === IntakeMode.Chat ? c.source_id : undefined)
    setSelected(null); setReviewed(false); setStep(Step.Edit)
    if (c.mode === IntakeMode.Template && c.source_id) {
      const template = await getBotTemplate(c.source_id)
      if (template.revision !== c.template_revision) throw new Error('Template changed. Your text is preserved; browse the template again and review the new revision.')
      setSelected(template); setTemplateBlocked(false)
    }
  }
  useEffect(() => {
    reset(); setSourceSession(initialSource); writer.current = null; setSavedSnapshot(''); setConflict(null)
    let cancelled = false
    if (resumeId) {
      setRestoring(true)
      void currentIntakeNamespace(scope, team).then(async namespace => {
        const next = new BotIntakeWriter(resumeId, scope, namespace, team)
        const saved = await next.load()
        if (!cancelled) { writer.current = next; await applyContent(saved.content); setSavedSnapshot(JSON.stringify(saved.content)) }
      }).catch(e => { if (!cancelled) setError(e instanceof Error ? e.message : String(e)) })
        .finally(() => { if (!cancelled) setRestoring(false) })
    }
    return () => { cancelled = true; lookup.current?.abort() }
  }, [reset, initialSource, resumeId, scope, team])
  useEffect(() => {
    if (!open) { lookup.current?.abort(); setLoading(false); setReviewed(false); setConfirmDiscard(false) }
  }, [open])
  useEffect(() => {
    if (!dirty || snapshot === savedSnapshot || created) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty, snapshot, savedSnapshot, created])
  const saveIntake = async (close = false) => {
    if (busy || restoring || loading || created || conflict) return
    if (!dirty) { if (close) onClose(); return }
    setBusy(true); setError('')
    try { await (await ensureWriter()).save(content); setSavedSnapshot(snapshot); if (close) onClose() }
    catch (e) { setError(`Intake not saved. Your text is still here. ${e instanceof Error ? e.message : String(e)}`) }
    finally { setBusy(false) }
  }
  const compareSaved = async () => {
    setBusy(true)
    try { setConflict((await (await ensureWriter()).load()).content) }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false) }
  }
  const cancel = () => {
    if (busy || restoring) return
    if (dirty || writer.current) { setConfirmDiscard(true); return }
    lookup.current?.abort(); onClose()
  }
  const discard = async () => {
    if (busy) return
    setBusy(true)
    try { await (await ensureWriter()).discard(); lookup.current?.abort(); reset(); writer.current = null; onClose() }
    catch (e) { setError(`Intake retained: ${e instanceof Error ? e.message : String(e)}`) }
    finally { setBusy(false) }
  }
  const finishCreation = async (bot: { id: string; scope: BotScope }) => {
    try { if (writer.current) await writer.current.discard() }
    catch (e) { setError(`Bot draft created; intake cleanup failed. Retry cleanup, not creation. ${e instanceof Error ? e.message : String(e)}`); return }
    reset(); setCreated(null)
    window.dispatchEvent(new Event('astonish:bots-updated'))
    window.location.hash = botPath(bot.id, bot.scope)
    onClose()
  }
  const loadTemplates = async (id?: string) => {
    lookup.current?.abort()
    const controller = new AbortController(); lookup.current = controller
    setLoading(true); setError('')
    try {
      if (id) {
        const template = await getBotTemplate(id, controller.signal)
        if (!controller.signal.aborted) { setSelected(template); setTemplateBlocked(false); setName(template.definition.name); setReviewed(false) }
      } else {
        const result = await listBotTemplates(controller.signal)
        if (!controller.signal.aborted) setTemplates(result.templates)
      }
    } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)) }
    finally { if (!controller.signal.aborted) setLoading(false) }
  }
  const valid = (!adaptive || !!adaptiveReview && adaptiveReview.source_job_id === adaptiveSource.trim()) && !templateBlocked && !!name.trim() && !!(selected?.definition.charter ?? charter).trim()
    && carryover.memories.every(m => m.content.trim() && m.category.trim())
    && carryover.goals.every(g => g.title.trim() && g.completion_criteria.trim())
    && carryover.artifacts.every(a => a.title.trim() && a.content.trim())
  const submit = async () => {
    if (!reviewed || busy || restoring || created || !valid || step !== Step.Review) return
    setBusy(true); setError('')
    const payload = JSON.stringify({ adaptive: adaptive ? { source: adaptiveSource.trim(), review: adaptiveReview, scope, team } : null, sourceSession, name, charter, carryover, persona, template: selected && { id: selected.id, revision: selected.revision } })
    if (attempt.current?.payload !== payload) attempt.current = { payload, key: crypto.randomUUID() }
    const key = attempt.current.key
    try {
      let bot: { id: string; scope: BotScope }
      if (adaptive || selected || sourceSession) {
        if (!sourceProposal || proposalAttempt.current?.snapshot !== snapshot) throw new Error('Review the saved source selection again before confirming.')
        const result = await decideSourceIntake(selected ? 'team' : scope, sourceProposal, 'confirm', adaptive && scope === 'team')
        setSourceProposal(result)
        if (result.status === 'stale') {
          proposalAttempt.current = null; setReviewed(false); setStep(Step.Edit)
          if (adaptive) setAdaptiveReview(null)
          throw new Error('Source changed. Fetch and review the current source before confirming; your edits are retained.')
        }
        if (!result.bot_id || result.status !== 'applied') throw new Error(`Source intake is ${result.status}. Retry this saved review; no replacement Bot was requested.`)
        bot = { id: result.bot_id, scope: selected ? 'personal' : result.scope }
      } else {
        bot = await createBot({ name, idempotency_key: key, definition: { charter, ...(JSON.stringify(persona) !== JSON.stringify(emptyPersona()) ? { persona } : {}) } })
      }
      setCreated(bot)
      // Creation already succeeded. Do not offer this intake for creation again after a reload,
      // even if the independent server-side cleanup needs a retry.
      writer.current?.forgetPointer()
      await finishCreation(bot)
    } catch (e) {
      if (adaptive && e instanceof BotAPIError && e.status === 409) {
        setAdaptiveReview(null); setReviewed(false); setStep(Step.Edit)
        setError('Source changed. Fetch a fresh source preview and review again; your edits are retained.')
      } else setError(`${adaptive ? 'Conversion did not return a confirmed result. No separate pause or activation was requested. Retry this request safely. ' : ''}${e instanceof Error ? e.message : String(e)}`)
    } finally { setBusy(false) }
  }
  const title = sourceSession ? 'Review Chat promotion' : 'Create a Bot draft'
  const description = 'Private, supervised, no contact or spending. Saving never activates a Bot. Only reviewed text is copied; source Chat history and credentials stay behind.'
  const fields = <>
    <p className="text-sm text-muted-foreground">{step === Step.Edit ? '1. Prepare content' : '2. Review exact draft'} · Back preserves your edits. Close or Escape saves unfinished intake privately for Resume intake after reload; it never creates or activates a Bot.</p>
    <fieldset disabled={busy || restoring || !!created} className="contents">
    {confirmDiscard && <section aria-label="Discard confirmation" className="space-y-2 rounded-md border p-3">
      <p>Discard this unfinished intake, including its saved copy? This cannot be undone.</p>
      <Button variant="outline" onClick={() => setConfirmDiscard(false)}>Keep editing</Button>
      <Button variant="destructive" disabled={busy} onClick={discard}>Discard intake</Button>
    </section>}
    {step === Step.Edit ? <>
      <label>Name<Input aria-label="Bot name" value={name} onChange={e => setName(e.target.value)} /></label>
      <label>Charter<Textarea aria-label="Bot charter" value={selected?.definition.charter ?? charter} disabled={!!selected} onChange={e => setCharter(e.target.value)} /></label>
      {!sourceSession && !selected && !adaptive && <fieldset className="space-y-2"><legend>Persona (optional)</legend>
        <label>Language<Input aria-label="Bot language" value={persona.language} onChange={e => setPersona({ ...persona, language: e.target.value })} /></label>
        <label>Timezone<Input aria-label="Bot timezone" value={persona.timezone} onChange={e => setPersona({ ...persona, timezone: e.target.value })} /></label>
        <label>Identity<Textarea aria-label="Bot identity" value={persona.identity} onChange={e => setPersona({ ...persona, identity: e.target.value })} /></label>
        <label>Working style<Textarea aria-label="Bot soul" value={persona.soul} onChange={e => setPersona({ ...persona, soul: e.target.value })} /></label>
        <label>Owner preferences<Textarea aria-label="Bot user preferences" value={persona.user} onChange={e => setPersona({ ...persona, user: e.target.value })} /></label>
      </fieldset>}
      {sourceSession && <BotCarryoverFields value={carryover} onChange={setCarryover} />}
      {!sourceSession && !selected && !adaptive && <Button variant="outline" disabled={loading} onClick={() => { setResumeMode(IntakeMode.Adaptive); setTemplates(null); setTemplateBlocked(false); restoredSource.current = {}; setReviewed(false) }}>Convert adaptive schedule</Button>}
      {adaptive && <Button variant="outline" onClick={() => { setResumeMode(IntakeMode.Blank); setAdaptiveSource(''); setAdaptiveReview(null); restoredSource.current = {}; setReviewed(false) }}>Use blank draft instead</Button>}
      {!sourceSession && !adaptive && <><Button variant="outline" disabled={loading} onClick={() => void loadTemplates()}>Browse team templates</Button>
        {loading && <p role="status">Loading templates…</p>}
        {templates?.length === 0 && <p>No team templates available. You can create a blank draft.</p>}
        {templates?.map(t => <Button key={t.id} disabled={loading} variant={selected?.id === t.id ? 'secondary' : 'ghost'} onClick={() => void loadTemplates(t.id)}>{t.definition.name} · revision {t.revision}</Button>)}
        {(selected || templateBlocked) && <Button variant="outline" disabled={loading} onClick={() => { setSelected(null); setTemplateBlocked(false); setResumeMode(IntakeMode.Blank); restoredSource.current = {} }}>Use blank draft instead</Button>}
      </>}
    </> : <section className="space-y-2 whitespace-pre-wrap break-words"><h3 className="font-semibold">{name}</h3><p>{selected?.definition.charter ?? charter}</p>
      {!sourceSession && !selected && !adaptive && <section aria-label="Persona review"><h4>Persona</h4>
        <p>Language: {persona.language || 'Default'} · Timezone: {persona.timezone || 'UTC'}</p>
        <p>{persona.identity || 'No custom identity.'}</p><p>{persona.soul || 'No custom working style.'}</p><p>{persona.user || 'No owner preferences.'}</p>
      </section>}
      <p>No contact, publishing or spending permission. Supervised autonomy; no active schedules. Runtime policy and schedule changes require separate review after saving. Saving neither tests nor activates the Bot.</p>
      {sourceSession && <><p>Source Chat: {sourceSession} (unchanged)</p><BotCarryoverFields value={carryover} onChange={setCarryover} review /></>}
    </section>}
    {adaptive && <BotAdaptiveConversion sourceId={adaptiveSource} scope={scope} value={adaptiveReview} onSourceChange={setAdaptiveSource} onReview={value => { setAdaptiveReview(value); setReviewed(false) }} reviewOnly={step === Step.Review} />}
    {selected && <section aria-label="Template review" className="space-y-2 text-sm whitespace-pre-wrap break-words"><h3 className="font-semibold">{selected.definition.name} · revision {selected.revision}</h3>
      <p>Language: {selected.definition.language || 'Default'}. Fork creates a separate private draft; all suggested schedules remain disabled.</p>
      {selected.definition.suggested_goals?.map((g, i) => <article key={i}><h4>{g.title}</h4><p>{g.description}</p><p>Completion: {g.completion_criteria}</p></article>)}
      {!selected.definition.suggested_goals?.length && <p>No suggested goals.</p>}
      {selected.definition.suggested_schedules?.map((s, i) => <article key={i}><p>{s.cron} · {s.timezone} · disabled</p><p>{s.instructions}</p></article>)}
      {!selected.definition.suggested_schedules?.length && <p>No suggested schedules.</p>}
    </section>}
    {step === Step.Review && <label className="flex gap-2 text-sm"><input type="checkbox" disabled={busy} checked={reviewed} onChange={e => setReviewed(e.target.checked)} />{adaptive ? `I reviewed this conversion${scope === 'team' ? ' for the team' : ''} and consent to pausing the original only after commit. This does not activate the Bot.` : 'I reviewed this content for the new private Bot.'}</label>}
    </fieldset>
    {restoring && <p role="status">Restoring unfinished intake…</p>}
    {conflict && <section aria-label="Saved intake comparison" className="space-y-2"><p>Your edits are unchanged above. Saved server copy:</p><pre className="whitespace-pre-wrap">{JSON.stringify(conflict, null, 2)}</pre><Button onClick={() => { void applyContent(conflict).catch(e => setError(String(e))); setConflict(null) }}>Replace local text with saved copy</Button><Button onClick={() => { setConflict(null); setError(''); setReviewed(false); setStep(Step.Edit) }}>Keep local text for next save</Button></section>}
    {error && <p role="alert" className="text-destructive">{error}</p>}
    {created ? <Button disabled={busy} onClick={() => void finishCreation(created)}>Retry intake cleanup</Button> : <>
    <div className="flex flex-wrap gap-2"><Button variant="ghost" disabled={busy || restoring} onClick={cancel}>Cancel intake</Button>
      <Button variant="outline" disabled={busy || restoring || !!conflict} onClick={() => void saveIntake(true)}>Save intake and close</Button>
      {error && writer.current && <Button variant="outline" disabled={busy} onClick={() => void compareSaved()}>Compare saved intake</Button>}
      {step === Step.Edit ? <Button disabled={!valid || loading || busy || restoring} onClick={async () => {
        setError(''); setReviewed(false)
        if (adaptive || selected || sourceSession) {
          setBusy(true)
          try {
            const intake = await ensureWriter()
            await intake.save(content); setSavedSnapshot(snapshot)
            const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(snapshot))
            const key = `${intake.id}:${Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('')}`
            const p = proposalAttempt.current?.snapshot === snapshot ? proposalAttempt.current.proposal : await proposeSourceIntake(selected ? 'team' : scope, { mode: adaptive ? 'adaptive' : selected ? 'template' : 'chat', source_id: adaptive ? adaptiveSource.trim() : selected?.id || sourceSession!, ...(selected ? { template_revision: selected.revision } : {}), name, charter: selected?.definition.charter || charter, carryover, idempotency_key: key })
            proposalAttempt.current = { snapshot, proposal: p }; setSourceProposal(p); setStep(Step.Review)
          } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setBusy(false) }
        } else setStep(Step.Review)
      }}>Review draft</Button> : <>
        <Button variant="outline" disabled={busy} onClick={() => { setReviewed(false); setStep(Step.Edit) }}>Back</Button>
        <Button onClick={submit} disabled={busy || !reviewed || !valid}>{busy ? 'Saving…' : 'Save reviewed draft'}</Button>
      </>}
    </div></>}
  </>
  if (inline) return open ? <section aria-label={title} tabIndex={-1} onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); void saveIntake(true) } }} className="border border-border rounded-xl p-4 m-3 space-y-3 max-h-[70dvh] overflow-y-auto"><h2 className="font-semibold">{title}</h2><p className="text-sm text-muted-foreground">{description}</p>{fields}</section> : null
  return <Dialog open={open} onOpenChange={v => { if (!v && !busy) { if (created) void finishCreation(created); else void saveIntake(true) } }}><DialogContent className="max-h-[90vh] overflow-y-auto"><DialogTitle>{title}</DialogTitle><DialogDescription>{description}</DialogDescription>{fields}</DialogContent></Dialog>
}
