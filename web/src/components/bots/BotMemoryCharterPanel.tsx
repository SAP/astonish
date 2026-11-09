import { useEffect, useState } from 'react'
import BotMemoryReviewEditor from './BotMemoryReviewEditor'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BotAPIError, type Bot } from '@/api/bots'
import { getBotDocuments, getBotMemoryHealth, saveBotDocument, submitBotCorrection, reconcileBotCorrection, type BotDocument, type BotDocuments, type BotMemoryHealth, type MemoryRecord, type CorrectionRequest } from '@/api/botMemory'

export interface BotMemoryCharterPanelProps { bot: Bot; readOnly: boolean; onChange?: () => void }
const message = (error: unknown) => error instanceof Error ? error.message : String(error)
const editableDocuments = new Set(['SOUL.md', 'IDENTITY.md', 'USER.md', 'CHARTER.md'])
const Stage = { Edit: 'edit', Review: 'review', Saving: 'saving', Conflict: 'conflict', Saved: 'saved' } as const
type DocumentStage = typeof Stage[keyof typeof Stage]

function DocumentEditor({ bot, document, revision, readOnly, onChange }: BotMemoryCharterPanelProps & { document: BotDocument; revision: number }) {
  const [content, setContent] = useState(document.content), [reason, setReason] = useState('')
  const [base, setBase] = useState({ revision, content: document.content })
  const [stage, setStage] = useState<DocumentStage>(Stage.Edit), [error, setError] = useState('')
  const [latest, setLatest] = useState<BotDocuments>(), [loading, setLoading] = useState(false)
  const locked = readOnly || stage === Stage.Saving || loading
  const editable = !document.read_only && editableDocuments.has(document.name)
  const edit = () => { if (stage !== Stage.Conflict) setStage(Stage.Edit); setError('') }
  const compare = async () => {
    setLoading(true); setError(''); setStage(Stage.Conflict)
    try { setLatest(await getBotDocuments(bot)) } catch (e) { setError(message(e)) } finally { setLoading(false) }
  }
  const save = async () => {
    if (locked || stage !== Stage.Review || !reason.trim()) return
    setStage(Stage.Saving); setError('')
    try {
      await saveBotDocument(bot, document.name, { expected_revision: base.revision, content, reason, confirmed: true })
      setStage(Stage.Saved); onChange?.()
    } catch (e) { setError(message(e)); setStage(e instanceof BotAPIError && e.status === 409 ? Stage.Conflict : Stage.Review) }
  }
  const remote = latest?.documents.find(d => d.name === document.name)
  return <article className="space-y-3 rounded border border-border bg-card p-4">
    <h3 className="font-medium">{document.name}</h3>
    <p className="text-sm text-muted-foreground">Source: {document.source_version || `Bot revision ${base.revision}`}</p>
    {editable ? <>
      <Textarea aria-label={`Content ${document.name}`} value={content} disabled={locked || stage === Stage.Saved} onChange={e => { setContent(e.target.value); edit() }} rows={6} />
      <Input aria-label={`Reason ${document.name}`} placeholder="Reason for this change" value={reason} disabled={locked || stage === Stage.Saved} onChange={e => { setReason(e.target.value); edit() }} />
      {stage === Stage.Review && <section aria-label={`Review ${document.name}`} className="space-y-2"><p>Review before saving. This changes descriptive content only, not permissions.</p><h4>Before</h4><pre className="whitespace-pre-wrap break-words">{base.content || '(empty)'}</pre><h4>After</h4><pre className="whitespace-pre-wrap break-words">{content || '(empty)'}</pre><Button disabled={locked} onClick={() => void save()}>Confirm save {document.name}</Button></section>}
      {stage !== Stage.Review && <Button disabled={locked || stage === Stage.Conflict || stage === Stage.Saved || !reason.trim() || content === base.content} onClick={() => setStage(Stage.Review)}>{stage === Stage.Saving ? 'Saving…' : `Review ${document.name}`}</Button>}
      {stage === Stage.Saved && <p role="status">Saved. Reload and compare before editing again.</p>}
      <Button variant="outline" disabled={locked} onClick={() => void compare()}>{loading ? 'Loading comparison…' : `Reload and compare ${document.name}`}</Button>
      {stage === Stage.Conflict && <p role="status">Local edits are preserved. Compare the current server version and explicitly choose a new baseline before reviewing again.</p>}
      {latest && <section className="space-y-2" aria-label={`Latest ${document.name}`}><p>Latest revision: {latest.revision}</p><pre className="whitespace-pre-wrap break-words">{remote?.content ?? 'Document is no longer available.'}</pre><Button variant="outline" disabled={locked || !remote || remote.read_only} onClick={() => { if (remote) { setBase({ revision: latest.revision, content: remote.content }); setLatest(undefined); setStage(Stage.Edit); setError('') } }}>Use latest revision; keep my edits {document.name}</Button></section>}
    </> : <><p className="text-sm text-muted-foreground">{document.write_unsupported_reason || 'Read-only projection; use reviewed record operations, not raw Markdown imports.'}</p><pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words">{document.content || 'Empty document.'}</pre>{document.record_mappings?.length ? <details><summary>Record mappings and fingerprints</summary><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(document.record_mappings, null, 2)}</pre></details> : null}</>}
    {!editable && !!document.record_mappings?.length && <details><summary>Prepare reviewed record ingest</summary><BotMemoryReviewEditor bot={{ ...bot, revision }} readOnly={readOnly} kind="document" document={document.name} sources={document.record_mappings} onChange={onChange} /></details>}
    {error && <p role="alert" className="text-destructive">{error}</p>}
  </article>
}

function CorrectionEditor({ bot, record, readOnly, onChange }: BotMemoryCharterPanelProps & { record: MemoryRecord }) {
  const [replacement, setReplacement] = useState(record.snippet), [summary, setSummary] = useState(''), [source, setSource] = useState('')
  const [revision, setRevision] = useState(bot.revision), [stage, setStage] = useState<DocumentStage>(Stage.Edit)
  const [error, setError] = useState(''), [request, setRequest] = useState<CorrectionRequest>(), [accepted, setAccepted] = useState(''), [applied, setApplied] = useState(false)
  const [latest, setLatest] = useState<{ revision: number; record?: MemoryRecord }>()
  const locked = readOnly || stage === Stage.Saving
  const edit = () => { if (stage !== Stage.Conflict) setStage(Stage.Edit); setRequest(undefined) }
  const review = () => {
    if (!record.id || locked || stage === Stage.Conflict || !replacement.trim() || !summary.trim() || !source.trim()) return
    setRequest({ id: crypto.randomUUID(), expected_revision: revision, superseded_source: `memory:${record.id}`, replacement, goal_ids: [], evidence: { summary, source, observed_at: new Date().toISOString() } }); setStage(Stage.Review)
  }
  const submit = async () => {
    if (!request || locked || stage !== Stage.Review) return
    setStage(Stage.Saving); setError('')
    try { const result = await submitBotCorrection(bot, request); setAccepted(result.id); setStage(Stage.Saved); onChange?.() }
    catch (e) { setError(message(e)); setStage(e instanceof BotAPIError && e.status === 409 ? Stage.Conflict : Stage.Review) }
  }
  const compare = async () => {
    setStage(Stage.Saving); setError('')
    try { const [docs, health] = await Promise.all([getBotDocuments(bot), getBotMemoryHealth(bot)]); setLatest({ revision: docs.revision, record: health.snapshot.records.find(r => r.id === record.id) }) }
    catch (e) { setError(message(e)) } finally { setStage(Stage.Conflict) }
  }
  const reconcile = async () => {
    if (!accepted || locked) return
    setStage(Stage.Saving); setError('')
    try { const result = await reconcileBotCorrection(bot, accepted); setApplied(result.memory_applied); if (!result.memory_applied) setError('Server has not confirmed memory application.'); onChange?.() }
    catch (e) { setError(message(e)) } finally { setStage(Stage.Saved) }
  }
  return <section aria-label="Correction" className="space-y-3 rounded border border-border p-3">
    <h3>Correct memory {record.id}</h3><p>Original evidence is retained. A correction records an owner-reviewed intent; application is a separate action. It grants no permissions.</p>
    <pre className="whitespace-pre-wrap">{record.snippet}</pre>
    {!accepted ? <>
      <fieldset disabled={locked} className="space-y-2"><legend>Correction details</legend>
        <Textarea aria-label="Replacement" value={replacement} onChange={e => { setReplacement(e.target.value); edit() }} />
        <Input aria-label="Correction evidence" placeholder="What did you verify?" value={summary} onChange={e => { setSummary(e.target.value); edit() }} />
        <Input aria-label="Evidence source" placeholder="Evidence source" value={source} onChange={e => { setSource(e.target.value); edit() }} />
      </fieldset>
      {stage === Stage.Review ? <><p>Review replacement and evidence for revision {revision}:</p><pre className="whitespace-pre-wrap">{replacement}</pre><p>{summary} — {source}</p><Button disabled={locked} onClick={() => void submit()}>Confirm correction</Button></> : <Button disabled={locked || stage === Stage.Conflict || !replacement.trim() || !summary.trim() || !source.trim()} onClick={review}>Review correction</Button>}
      {stage === Stage.Conflict && <><p>Conflict: your replacement and evidence are preserved.</p><Button onClick={() => void compare()}>Reload and compare correction</Button></>}
      {latest && <><p>Latest source at revision {latest.revision}</p><pre className="whitespace-pre-wrap">{latest.record?.snippet || 'Source is unavailable in the current bounded snapshot.'}</pre><Button disabled={locked || !latest.record} onClick={() => { setRevision(latest.revision); setLatest(undefined); setRequest(undefined); setStage(Stage.Edit) }}>Use latest revision; keep correction</Button></>}
    </> : <><p role="status">{applied ? 'Memory correction applied.' : `Recorded for reconciliation (${accepted}); memory is not yet confirmed applied.`}</p>{!applied && <Button disabled={locked} onClick={() => void reconcile()}>Apply saved correction to memory</Button>}</>}
    {stage === Stage.Saving && <p role="status">Saving…</p>}{error && <p role="alert" className="text-destructive">{error}</p>}
  </section>
}

function Panel({ bot, readOnly, onChange }: BotMemoryCharterPanelProps) {
  const [documents, setDocuments] = useState<BotDocuments>(), [health, setHealth] = useState<BotMemoryHealth>()
  const [documentError, setDocumentError] = useState(''), [healthError, setHealthError] = useState('')
  const [documentAttempt, setDocumentAttempt] = useState(0), [healthAttempt, setHealthAttempt] = useState(0)
  const [correction, setCorrection] = useState<MemoryRecord>()
  const [healthLoading, setHealthLoading] = useState(true)
  const { id, scope } = bot
  useEffect(() => {
    const controller = new AbortController(); setDocumentError('')
    getBotDocuments({ id, scope }, controller.signal).then(value => { if (!controller.signal.aborted) setDocuments(value) }).catch(e => { if (!controller.signal.aborted) setDocumentError(message(e)) })
    return () => controller.abort()
  }, [id, scope, documentAttempt]) // Revision changes must not discard a local draft.
  useEffect(() => {
    const controller = new AbortController(); setHealthError(''); setHealthLoading(true)
    getBotMemoryHealth({ id, scope }, controller.signal).then(value => { if (!controller.signal.aborted) setHealth(value) }).catch(e => { if (!controller.signal.aborted) setHealthError(message(e)) }).finally(() => { if (!controller.signal.aborted) setHealthLoading(false) })
    return () => controller.abort()
  }, [id, scope, healthAttempt])
  return <section className="space-y-5 text-foreground">
    <h2 className="font-semibold">Memory &amp; persona documents</h2>
    <p className="text-sm text-muted-foreground">Descriptive prose never grants rights, changes tool policy, or confirms evidence. Current memory and goals are not historical persona snapshots.</p>
    {readOnly && <p>Read-only access. Changes require an authorized owner or administrator.</p>}
    {documentError ? <div role="alert">{documentError}<Button variant="outline" onClick={() => setDocumentAttempt(n => n + 1)}>Retry documents</Button></div> : !documents && <p role="status">Loading documents…</p>}
    {documents && <><p>Version {documents.version} · revision {documents.revision}</p>{!documents.documents.length && <p>No documents available.</p>}{documents.documents.map(document => <DocumentEditor key={document.name} bot={bot} document={document} revision={documents.revision} readOnly={readOnly} onChange={onChange} />)}</>}
    <section className="space-y-3"><h2 className="font-semibold">Memory Health</h2><Button variant="outline" disabled={healthLoading} onClick={() => setHealthAttempt(n => n + 1)}>{healthLoading ? 'Refreshing Memory Health…' : 'Refresh Memory Health'}</Button>
      {healthError ? <div role="alert">{healthError}<Button variant="outline" onClick={() => setHealthAttempt(n => n + 1)}>Retry Memory Health</Button></div> : !health && <p role="status">Loading Memory Health…</p>}
      {health && <><p>Evaluated: {health.snapshot.evaluated_at} · Scope: {health.scope}</p><p>Recommendations are review-only; never automatically applied.</p>{health.snapshot.truncated && <p role="status">Bounded snapshot: additional memory records are omitted.</p>}
        {!health.health.recommendations?.length && <p>No health recommendations.</p>}
        {health.health.recommendations?.map(item => <article key={item.id} className="rounded border border-border p-3"><h3>{item.title} · {item.severity}</h3><p>{item.description}</p><p>Source memory IDs: {item.memory_ids?.join(', ') || 'None'}</p><details><summary>Review recommendation data</summary><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(item.card, null, 2)}</pre></details>{health.apply_supported && <details><summary>Prepare reviewed consolidation</summary><BotMemoryReviewEditor bot={{ ...bot, revision: documents?.revision ?? bot.revision }} readOnly={readOnly} kind="consolidate" sources={health.snapshot.records.flatMap(r => r.id && r.session_id && item.memory_ids?.includes(r.id) && health.snapshot.fingerprints[r.id] ? [{ id: r.id, session_id: r.session_id, expected_fingerprint: health.snapshot.fingerprints[r.id] }] : [])} onChange={onChange} /></details>}</article>)}
        {!health.snapshot.records.length && <p>No memory records yet.</p>}
        {health.snapshot.records.map((record, i) => <article key={record.id || i} className="space-y-2 rounded border border-border p-3"><pre className="whitespace-pre-wrap break-words">{record.snippet}</pre><p>ID: {record.id || 'Unavailable'} · Category: {record.category || 'Uncategorized'}</p><p>Session: {record.session_id || 'Unknown'} · Creator: {record.created_by || 'Unknown'}</p><p>Source: {record.path} · Created: {record.created_at || 'Unknown'}</p>{record.attribution && <details><summary>Provenance</summary><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(record.attribution, null, 2)}</pre></details>}<Button variant="outline" disabled={readOnly || !record.id || !!correction} onClick={() => setCorrection(record)}>Correct {record.id || 'memory'}</Button></article>)}
      </>}
      {correction && <CorrectionEditor key={correction.id} bot={{ ...bot, revision: documents?.revision ?? bot.revision }} record={correction} readOnly={readOnly} onChange={onChange} />}
    </section>
  </section>
}

// Identity changes remount local state; ordinary revision refreshes preserve edits.
export default function BotMemoryCharterPanel(props: BotMemoryCharterPanelProps) {
  return <Panel key={`${props.bot.scope}:${props.bot.id}`} {...props} />
}
