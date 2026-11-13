import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { getBotVersion, type Bot, type BotTrigger } from '@/api/bots'
import * as api from '@/api/botSkills'
import { useWorkspaceResource } from './workspaceResource'
import { botPath } from './botRoute'

export default function BotSkillsPanel({ bot, revision = 0 }: { bot: Bot; revision?: number }) {
  const [after, setAfter] = useState('')
  const [archived, setArchived] = useState(false)
  const [selected, setSelected] = useState<{ identity: string; skill: api.BotSkill }>()
  const identity = `${bot.scope}/${bot.id}`
  const resource = useWorkspaceResource(signal => api.listBotSkills(bot, signal, { after, include_archived: archived }), `${identity}/${after}/${archived}`, revision)
  return <section aria-label="Bot skills" className="space-y-3">
    <h2 className="font-semibold">Skills &amp; history</h2>
    <p className="text-sm text-muted-foreground">Committed operating knowledge, not chat claims or authorization. Active means available for use, not independently verified. History inspection is read-only; legacy import requires confirmation.</p>
    <label className="flex gap-2"><input type="checkbox" checked={archived} onChange={e => { setArchived(e.target.checked); setAfter('') }} />Include archived skills</label>
    <Button variant="outline" onClick={resource.retry}>Refresh skills</Button>
    {resource.loading && <p role="status">Loading skills…</p>}
    {resource.error && <p role="alert">{resource.data ? 'Stale skills — refresh before relying on these results. ' : ''}{resource.error}</p>}
    {resource.data?.skills.length === 0 && <p>No committed skills yet. Legacy schedules remain available in Schedule &amp; triggers; viewing them does not import a skill.</p>}
    {resource.data?.skills.map(skill => <article key={skill.id} className="border border-border rounded p-3">
      <Button variant="link" disabled={resource.loading || !!resource.error} onClick={() => setSelected({ identity, skill })}>{skill.title}</Button>
      <p>{skill.status} · revision {skill.head_revision}</p>
      <p className="break-all text-xs">{skill.head_digest}</p>
      {skill.legacy && <p>Imported legacy trigger: {skill.legacy.trigger_id} · version {skill.legacy.version}</p>}
    </article>)}
    {after && <Button variant="outline" onClick={() => setAfter('')}>First skills page</Button>}
    {resource.data?.next && <Button variant="outline" disabled={resource.loading || !!resource.error} onClick={() => setAfter(resource.data!.next!)}>More skills</Button>}
    <LegacyImport key={`${identity}/${bot.version}/${bot.revision}`} bot={bot} onImported={resource.retry} />
    {selected?.identity === identity && <SkillDetails key={`${identity}/${selected.skill.id}`} bot={bot} skill={selected.skill} revision={revision} onClose={() => setSelected(undefined)} />}
  </section>
}
function LegacyImport({ bot, onImported }: { bot: Bot; onImported: () => void }) {
  const [open, setOpen] = useState(false)
  return open ? <LegacyImportReview bot={bot} onImported={onImported} onClose={() => setOpen(false)} /> : <Button variant="outline" onClick={() => setOpen(true)}>Review legacy import</Button>
}
function LegacyImportReview({ bot, onImported, onClose }: { bot: Bot; onImported: () => void; onClose: () => void }) {
  const source = useWorkspaceResource(signal => getBotVersion(bot.id, bot.version, bot.scope, signal), `${bot.scope}/${bot.id}/${bot.version}`, 0)
  const [choice, setChoice] = useState<{ trigger: BotTrigger; key: string }>()
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [success, setSuccess] = useState('')
  const confirm = async () => {
    if (!choice || busy) return
    setBusy(true); setError('')
    try { const result = await api.importLegacyBotSkill(bot, choice.trigger, choice.key); setSuccess(`Imported ${result.title} as a draft skill. Schedule and grants remain unchanged.`); setChoice(undefined); onImported() }
    catch (e) { setError(`Import not confirmed. Refresh Bot details if the source changed. ${e instanceof Error ? e.message : String(e)}`) }
    finally { setBusy(false) }
  }
  const triggers = source.data?.triggers?.filter(t => !t.skill_id && t.instructions.trim()) || []
  return <section aria-label="Legacy import review" className="space-y-2 border p-3">
    <h3>Explicit legacy import</h3><p>Creates draft knowledge only. Does not enable a skill, run work, alter schedules, or grant authorization.</p>
    {source.loading && <p role="status">Loading legacy procedures…</p>}
    {source.error && <><p role="alert">{source.error}</p><Button onClick={source.retry}>Retry legacy read</Button></>}
    {!source.loading && !source.error && !triggers.length && <p>No legacy procedures available.</p>}
    {triggers.map(t => <div key={t.id}><p>{t.instructions}</p><Button disabled={busy || !!source.error} onClick={() => { setChoice({ trigger: t, key: crypto.randomUUID() }); setSuccess(''); setError('') }}>Select legacy procedure {t.id}</Button></div>)}
    {choice && <><p>Confirm import of: {choice.trigger.instructions}</p><Button disabled={busy || !!source.error} onClick={() => void confirm()}>Confirm legacy import</Button></>}
    {busy && <p role="status">Importing…</p>}{error && <p role="alert">{error}</p>}{success && <p role="status">{success}</p>}
    <Button disabled={busy} onClick={onClose}>Cancel legacy import</Button>
  </section>
}
function Provenance({ value }: { value: api.SkillProvenance }) {
  return <p className="text-xs break-words">{value.level} · {value.recorded_at} {value.run_id && `· run ${value.run_id}`} {value.event_id && `· event ${value.event_id}`} {value.quote && `· ${value.quote}`} {value.evidence_refs?.join(', ')}</p>
}
export function SkillRevision({ value }: { value: api.BotSkillRevision }) {
  if (value.schema_version !== 1) return <p role="status">Unsupported skill schema {value.schema_version}. Content is unavailable; no interpretation attempted.</p>
  return <article className="space-y-2 border border-border rounded p-3">
    <h4>Revision {value.revision}</h4><p className="break-all text-xs">Digest: {value.digest} · parent {value.parent_revision}</p>
    <p>Responsibility: {value.responsibility}</p>
    <h5>Expectations</h5>{value.expectations?.map(item => <div key={item.id}><p>{item.text}</p><Provenance value={item.provenance} /></div>)}
    <h5>Method</h5><ol className="list-decimal pl-5">{value.method?.map((text, i) => <li key={i}>{text}</li>)}</ol>
    <p>Source usefulness is agent judgment, not established by successful access.</p><h5>Sources</h5>{value.sources?.map((source, i) => <div key={i} className="break-words"><p>{source.url} — {source.purpose}</p><p>Access: {source.access_method} · fallback: {source.fallback || 'none'} · limitation: {source.limitation || 'none recorded'} · observed: {source.last_observed_at || 'not observed'}</p>{source.observation ? <p>Access observation: {source.observation.status}{source.observation.http_status ? ` · HTTP ${source.observation.http_status}` : ''} · call {source.observation.call_id}{source.observation.truncated ? ' · truncated' : ''}</p> : <p>Access observation: unavailable</p>}<Provenance value={source.provenance} /></div>)}
    <p>Output format: {value.output_format || 'not specified'}</p><p>Evaluation rules: {value.evaluation_rules?.join('; ') || 'not specified'}</p>
    <p>Change reason: {value.change_reason}</p>{value.reverts_revision && <p>Reverts revision {value.reverts_revision}</p>}
    <h5>Provenance</h5>{value.provenance?.map((p, i) => <Provenance key={i} value={p} />)}
  </article>
}
function SkillDetails({ bot, skill, revision, onClose }: { bot: Bot; skill: api.BotSkill; revision: number; onClose: () => void }) {
  const [after, setAfter] = useState(''), [runsAfter, setRunsAfter] = useState('')
  const key = `${bot.scope}/${bot.id}/${skill.id}`
  const history = useWorkspaceResource(signal => api.listBotSkillRevisions(bot, skill.id, signal, { after }), `${key}/history/${after}`, revision)
  const runs = useWorkspaceResource(signal => api.listBotSkillRuns(bot, skill.id, signal, { after: runsAfter }), `${key}/runs/${runsAfter}`, revision)
  return <section aria-label="Skill history" className="space-y-3">
    <h3>{skill.title} history</h3><Button variant="outline" onClick={onClose}>Close skill</Button>
    <Button variant="outline" onClick={() => { history.retry(); runs.retry() }}>Refresh skill history</Button>
    {history.loading && <p role="status">Loading revisions…</p>}{history.error && <p role="alert">{history.data ? 'Stale revisions. ' : ''}{history.error}</p>}
    {history.data?.revisions.length === 0 && <p>No committed revisions.</p>}
    {history.data?.revisions.map(value => <SkillRevision key={value.revision} value={value} />)}
    {after && <Button variant="outline" onClick={() => setAfter('')}>First revisions page</Button>}
    {history.data?.next && <Button variant="outline" disabled={history.loading || !!history.error} onClick={() => setAfter(history.data!.next!)}>More revisions</Button>}
    <h4>Run receipts</h4>{runs.loading && <p role="status">Loading skill runs…</p>}{runs.error && <p role="alert">{runs.data ? 'Stale receipts. ' : ''}{runs.error}</p>}
    {runs.data?.runs.length === 0 && <p>No linked runs.</p>}
    {runs.data?.runs.map(run => <article key={run.id} className="border border-border rounded p-3"><a href={`#${botPath(bot.id, bot.scope, run.id)}`}>Run {run.id} · {run.status}</a>
      {run.report.skill?.schema_version === 1 ? <><p>{run.report.skill.kind} · used revision {run.report.skill.used_revision} · committed revision {run.report.skill.committed_revision || 'none'}</p><p>{run.report.skill.reason}</p><p className="break-all text-xs">{run.report.skill.digest}</p></> : <p>Skill receipt unavailable or unsupported.</p>}
    </article>)}
    {runsAfter && <Button variant="outline" onClick={() => setRunsAfter('')}>First runs page</Button>}
    {runs.data?.next && <Button variant="outline" disabled={runs.loading || !!runs.error} onClick={() => setRunsAfter(runs.data!.next!)}>More skill runs</Button>}
  </section>
}
