import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BotAPIError, type Bot } from '@/api/bots'
import { applyBotMemoryReview, getBotDocuments, getBotMemoryHealth, type MemoryReview, type MemoryReviewSource, type MemoryReviewReceipt, type MemoryRecord } from '@/api/botMemory'

interface Props { bot: Bot; readOnly: boolean; kind: MemoryReview['kind']; document?: string; sources: MemoryReviewSource[]; onChange?: () => void }
const Stage = { Edit: 'edit', Review: 'review', Busy: 'busy', Conflict: 'conflict', Saved: 'saved' } as const
export default function BotMemoryReviewEditor({ bot, readOnly, kind, document, sources, onChange }: Props) {
  const [content, setContent] = useState(''), [reason, setReason] = useState(''), [category, setCategory] = useState('note')
  const [stage, setStage] = useState<typeof Stage[keyof typeof Stage]>(Stage.Edit), [error, setError] = useState('')
  const [baseline, setBaseline] = useState({ revision: bot.revision, sources })
  const [comparison, setComparison] = useState<{ revision: number; sources: MemoryReviewSource[]; records: MemoryRecord[] }>()
  const [request, setRequest] = useState<MemoryReview>(), [receipt, setReceipt] = useState<MemoryReviewReceipt>()
  const locked = readOnly || stage === Stage.Busy || stage === Stage.Saved
  const edit = () => { if (stage !== Stage.Conflict) setStage(Stage.Edit); setRequest(undefined) }
  const valid = content.trim() && reason.trim() && baseline.sources.length >= (kind === 'consolidate' ? 2 : 1) && baseline.sources.length <= 100
  const review = () => {
    if (locked || !valid || stage === Stage.Conflict) return
    setRequest({ id: crypto.randomUUID(), expected_revision: baseline.revision, kind, ...(document ? { document } : {}), reason, confirmed: true, edits: [{ session_id: baseline.sources[0].session_id, sources: baseline.sources, content, category }] })
    setStage(Stage.Review)
  }
  const save = async () => {
    if (!request || locked || stage !== Stage.Review) return
    setStage(Stage.Busy); setError('')
    try { setReceipt(await applyBotMemoryReview(bot, request)); setStage(Stage.Saved); onChange?.() }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); setStage(e instanceof BotAPIError && e.status === 409 ? Stage.Conflict : Stage.Review) }
  }
  const compare = async () => {
    setStage(Stage.Busy); setError('')
    try {
      const [docs, health] = await Promise.all([getBotDocuments(bot), getBotMemoryHealth(bot)])
      const records = health.snapshot.records.filter(r => baseline.sources.some(s => s.id === r.id))
      const mappings = records.flatMap(r => r.id && r.session_id && health.snapshot.fingerprints[r.id] ? [{ id: r.id, session_id: r.session_id, expected_fingerprint: health.snapshot.fingerprints[r.id] }] : [])
      setComparison({ revision: docs.revision, sources: mappings, records })
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) } finally { setStage(Stage.Conflict) }
  }
  return <section aria-label="Reviewed memory mappings" className="space-y-3 rounded border border-border p-3">
    <h3>{kind === 'consolidate' ? 'Review consolidation' : `Review ingest: ${document}`}</h3>
    <p>Creates attributed replacement records; original evidence is retained. No permissions or verified status come from this text.</p>
    <p>Target session: {baseline.sources[0]?.session_id || 'Unavailable'} · Bot revision: {baseline.revision}</p>
    <details><summary>Source mappings ({baseline.sources.length})</summary><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(baseline.sources, null, 2)}</pre></details>
    <fieldset disabled={locked} className="space-y-2"><legend>Proposed record</legend>
      <Textarea aria-label="Reviewed memory content" rows={5} maxLength={100000} value={content} onChange={e => { setContent(e.target.value); edit() }} />
      <Input aria-label="Memory category" value={category} onChange={e => { setCategory(e.target.value); edit() }} />
      <Input aria-label="Memory review reason" placeholder="Reason and evidence for this reviewed change" value={reason} onChange={e => { setReason(e.target.value); edit() }} />
    </fieldset>
    {stage === Stage.Review ? <><h4>Confirm exact replacement and reason</h4><pre className="whitespace-pre-wrap">{content}</pre><p>{category} · {reason}</p><Button disabled={locked} onClick={() => void save()}>Confirm mapped changes</Button></> : <Button disabled={locked || !valid || stage === Stage.Conflict} onClick={review}>Review mapped changes</Button>}
    {stage === Stage.Busy && <p role="status">Saving or loading comparison…</p>}
    {stage === Stage.Conflict && <><p>Conflict: local content is preserved; review current source records before rebasing.</p><Button disabled={locked} onClick={() => void compare()}>Reload mapped sources for comparison</Button></>}
    {comparison && <><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(comparison.records, null, 2)}</pre>{comparison.sources.length !== baseline.sources.length && <p>Some sources are unavailable in this bounded snapshot. Rebase is blocked.</p>}<Button disabled={locked || comparison.sources.length !== baseline.sources.length} onClick={() => { setBaseline(comparison); setComparison(undefined); setRequest(undefined); setStage(Stage.Edit) }}>Use compared source fingerprints</Button></>}
    {receipt && <div role="status"><p>Saved reviewed records: {receipt.replacement_ids.join(', ')}</p><details><summary>Receipt and preserved provenance</summary><pre className="overflow-auto whitespace-pre-wrap">{JSON.stringify(receipt, null, 2)}</pre></details></div>}
    {error && <p role="alert" className="text-destructive">{error}</p>}
  </section>
}
