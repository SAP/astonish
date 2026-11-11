import { useCallback, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import * as api from '@/api/bots'
import { listControlProposals, sendBotMessage } from '@/api/botReviews'
import BotEmployeeMessage from './BotEmployeeMessage'
import { botLocalFailure } from '../chat/botEmployeeFeedback'
import StudioChat from '../StudioChat'
import { BotLifecycleControls, BotPolicySummary } from './BotControls'
import { BotGoals, BotMemoryCharterPanel, BotSchedule, BotTemplatePublication } from './BotPanels'
import BotConfigurationPanel from './BotConfigurationPanel'
import BotStopRun from './BotStopRun'
import BotHomeSetup from './BotHomeSetup'
import BotHomeModel from './BotHomeModel'
import BotTestOnce from './BotTestOnce'
import { botPath, type BotRoute } from './botRoute'
import { useBotSnapshot } from './useBotSnapshot'
import { getActiveTeam } from '@/api/teamContext'
import { useWorkspaceResource as useResource } from './workspaceResource'
import BotContinueInteractive from './BotContinueInteractive'
import { RunStatus } from '@/api/botRunState'
import { BotReviewCard } from './BotReviewCard'
import type { BotOperationView } from '@/api/botOperationResult'

const conversationReadOnly = (bot: api.Bot, userId?: string) => {
  const readOnly = !userId || bot.owner_id !== userId
  return bot.status === api.BotStatus.Deleted || bot.status === api.BotStatus.Stopped || ((bot.status === api.BotStatus.Draft || bot.status === api.BotStatus.Paused) ? readOnly : !userId || (bot.scope === 'personal' && readOnly))
}

export default function BotWorkspace({ route, theme, userId }: { route: BotRoute; theme: string; userId?: string }) {
  const snapshot = useBotSnapshot(route.botId, route.scope, userId, true)
  const [revision, setRevision] = useState(0)
  const refresh = useCallback(() => { snapshot.retry(); setRevision(v => v + 1); window.dispatchEvent(new Event('astonish:bots-updated')) }, [snapshot.retry])
  const key = `${userId || ''}/${getActiveTeam() || ''}/${route.scope}/${route.botId}`
  if (!snapshot.data) return <section className="p-4"><p role={snapshot.error ? 'alert' : 'status'}>{snapshot.error || 'Loading Bot…'}</p><Button onClick={refresh}>Retry workspace</Button></section>
  return <LoadedBotWorkspace key={key} bot={snapshot.data.bot} route={route} theme={theme} userId={userId} refresh={refresh} revision={revision + snapshot.data.latest_sequence} snapshot={snapshot} />
}
function LoadedBotWorkspace({ bot, route, theme, userId, refresh, revision, snapshot }: { bot: api.Bot; route: BotRoute; theme: string; userId?: string; refresh: () => void; revision: number; snapshot: ReturnType<typeof useBotSnapshot> }) {
  const [details, setDetails] = useState(!!route.runId)
  const proposals = useResource(s => listControlProposals(bot, s), bot.id, revision)
  const approvals = useResource(s => api.listBotApprovals(bot.id, bot.scope, s), bot.id, revision)
  const request = useRef<{ signature: string; key: string } | null>(null)
  const readOnly = !userId || bot.owner_id !== userId
  const toolFree = false
  const conversationUnavailable = conversationReadOnly(bot, userId)
  const allApprovals = [...(approvals.data?.approvals || snapshot.data?.pending_approvals || [])].sort((a, b) => Date.parse(a.expires_at) - Date.parse(b.expires_at))
  const onOperationsChange = useCallback((operations: BotOperationView[]) => {
    // Background reconciliation changes the root, never replays user input.
    void snapshot.syncOperations(operations).catch(() => { /* Send reports an unresolved projection; transport errors already surface in the snapshot. */ })
  }, [snapshot.syncOperations])
  const send = async (text: string, attachments?: import('@/api/studioChat').AttachmentPayload[], operations: BotOperationView[] = []) => {
    if (conversationUnavailable) throw new Error('This Bot conversation is read-only.')
    if (toolFree && attachments?.length) throw new Error('Attachments are unavailable in tool-free conversation.')
    const current = await snapshot.syncOperations(operations)
    if (conversationReadOnly(current, userId)) throw new Error('This Bot conversation is read-only.')
    const signature = JSON.stringify([current.id, current.version, text, attachments])
    if (request.current?.signature !== signature) request.current = { signature, key: crypto.randomUUID() }
    const receipt = await sendBotMessage(current, text, request.current.key, attachments)
    request.current = null
    refresh()
    return receipt
  }
  const renderReview = (kind: 'proposal' | 'approval', id: string) => <BotReviewCard key={`${kind}/${id}`} bot={bot} proposal={kind === 'proposal' ? proposals.data?.proposals.find(p => p.id === id) : undefined} approval={kind === 'approval' ? allApprovals.find(a => a.id === id) : undefined} requiresContinueInteractive={snapshot.data?.run_states?.some(s => s.run_id === allApprovals.find(a => a.id === id)?.run_id && s.reason === 'continuation_required')} readOnly={readOnly || !!snapshot.error || !!proposals.error || !!approvals.error || bot.status === api.BotStatus.Deleted} onChange={refresh} />
  return <section aria-label="Bot workspace" className="flex flex-col flex-1 min-w-0 min-h-0 overflow-hidden bg-background text-foreground">
    <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-2"><div className="min-w-0"><h1 className="font-semibold truncate">{bot.name}</h1><p className="text-xs text-muted-foreground">{bot.status} · {bot.scope === 'personal' ? 'Private' : 'Team'}</p></div>
      <Dialog modal={false} open={details} onOpenChange={setDetails}><DialogTrigger asChild><Button variant="ghost">Details</Button></DialogTrigger><DialogPrimitive.Portal forceMount><DialogPrimitive.Overlay className={details ? 'fixed inset-0 z-50 bg-black/60' : 'hidden'} /><DialogPrimitive.Content forceMount aria-hidden={!details} style={!details ? { display: 'none' } : undefined} className="fixed top-[50%] left-[50%] translate-x-[-50%] translate-y-[-50%] z-50 rounded-xl border bg-card p-6 shadow-xl w-[calc(100%-2rem)] sm:max-w-3xl max-h-[90dvh] overflow-y-auto"><DialogTitle>{bot.name} details</DialogTitle><DialogDescription>Settings, history and exceptional decisions. Routine work reports its verified result in Chat. Closing Details preserves unfinished edits until you leave this Bot.</DialogDescription><DialogPrimitive.Close asChild><Button variant="ghost">Close</Button></DialogPrimitive.Close><BotDetails bot={bot} readOnly={readOnly} revision={revision} refresh={refresh} snapshot={snapshot} route={route} reviews={<>{proposals.error && <p role="alert">{proposals.error}<Button onClick={proposals.retry}>Retry reviews</Button></p>}{proposals.data?.proposals.map(p => renderReview('proposal', p.id))}{allApprovals.filter(a => a.status === 'pending' || a.status === 'approved').map(a => renderReview('approval', a.id))}</>} /></DialogPrimitive.Content></DialogPrimitive.Portal></Dialog>
    </header>
    {snapshot.error && <p role="alert" className="px-4 text-sm">{snapshot.error} <Button variant="link" onClick={refresh}>Retry</Button></p>}
    {snapshot.data?.run_states?.filter(s => s.reason === 'continuation_required' && (s.status === RunStatus.Interrupted || s.status === RunStatus.Queued) && !allApprovals.some(a => a.run_id === s.run_id && a.status === 'pending')).map(s => <BotContinueInteractive key={`${bot.id}/${s.run_id}/${s.state_sequence}`} bot={bot} runId={s.run_id} disabled={conversationUnavailable || readOnly || !!snapshot.error || !!approvals.error} onChange={refresh} />)}
    <StudioChat key={bot.id} theme={theme} initialSessionId={bot.home_session_id} hideSidebar botThread={{ botId: bot.id, scope: bot.scope, status: bot.status, readOnly: conversationUnavailable, toolFree, operations: snapshot.data?.operations || [], decisionsReadOnly: readOnly || !!snapshot.error, runStates: snapshot.data?.run_states, onSend: send, onOperationsChange, renderReview }} />
  </section>
}
export function BotQualityDetails({ quality, unavailable = false }: { quality?: api.BotQualityReport; unavailable?: boolean }) {
  if (unavailable) return <p role="status">Assessment unavailable</p>
  return <details aria-label="Quality assessment"><summary>Quality assessment: {api.botQualitySummary(quality)}</summary>
    {quality?.schema_version === 1 && <div className="space-y-2 text-sm">
      {(quality.criteria || []).map(c => <div key={c.criterion_id}><p>{c.criterion_id}: {c.verdict} — {c.explanation}</p><p className="text-muted-foreground">{c.method === api.BotAssessmentMethod.Mechanical ? 'Mechanical observation' : c.method === api.BotAssessmentMethod.Model ? 'Model assessment' : 'Assessment method unavailable'}</p><p className="break-words">Evidence: {(c.evidence_refs || []).join(', ') || 'Unavailable'}</p></div>)}
      {(quality.limitations || []).map((text, i) => <p key={i}>Unresolved: {text}</p>)}
    </div>}
  </details>
}

function BotDetails({ bot, readOnly, revision, refresh, snapshot, reviews, route }: { bot: api.Bot; readOnly: boolean; revision: number; refresh: () => void; snapshot: ReturnType<typeof useBotSnapshot>; reviews: React.ReactNode; route: BotRoute }) {
  const version = useResource(s => api.getBotVersion(bot.id, bot.version, bot.scope, s), bot.id, revision)
  const runs = useResource(s => api.listBotRuns(bot.id, bot.scope, s), bot.id, revision)
  const goals = useResource(s => api.listBotGoals(bot.id, bot.scope, s), bot.id, revision)
  const [prepared, setPrepared] = useState<api.Bot>()
  const effectiveBot = prepared && prepared.revision > bot.revision ? prepared : bot
  const unavailable = readOnly || bot.status === api.BotStatus.Deleted
  const [error, setError] = useState('')
  return <div className="space-y-4"><p className="text-xs">Version {effectiveBot.version} · revision {effectiveBot.revision}</p><Button variant="outline" onClick={refresh}>Refresh Bot</Button>
    {version.error && <p role="alert">{version.error}<Button onClick={version.retry}>Retry policy</Button></p>}{version.data && <BotPolicySummary version={version.data} />}
    <BotLifecycleControls bot={bot} readOnly={readOnly || !version.data || !!snapshot.error} onChange={refresh} />
    <details><summary>Execution setup</summary><BotHomeSetup bot={effectiveBot} disabled={unavailable} onPrepared={next => { setPrepared(next); refresh() }} onRefresh={refresh} />{!readOnly && bot.home_session_id && <BotHomeModel sessionId={bot.home_session_id} />}<BotTestOnce bot={effectiveBot} disabled={unavailable || !version.data || !effectiveBot.home} onRun={run => { refresh(); window.location.hash = botPath(bot.id, bot.scope, run.id) }} /></details>
    <details open={!!route.runId}><summary>Runs & activity</summary><p>{snapshot.data?.unread_count || 0} unread updates · {snapshot.data?.pending_approvals.length || 0} decisions waiting</p>{snapshot.data?.latest_outcome ? <><p>{snapshot.data.latest_outcome.report.summary || snapshot.data.latest_outcome.status}</p><BotQualityDetails quality={snapshot.data.latest_outcome.report.quality} unavailable={!!snapshot.error} /></> : <p>No assessed result yet.</p>}{runs.error && <p role="alert">{runs.error}<Button onClick={runs.retry}>Retry runs</Button></p>}{runs.data?.runs.length === 0 && <p>No runs yet.</p>}{runs.data?.runs.filter(run => run.id !== route.runId).map(run => <article key={run.id} className="border rounded p-3"><a href={`#${botPath(bot.id, bot.scope, run.id)}`}>{run.status} · {run.trigger}</a><p>{run.report.summary || 'No report yet.'}</p><BotQualityDetails quality={run.report.quality} unavailable={!!runs.error} /><BotStopRun bot={bot} run={run} disabled={unavailable} onChange={refresh} /><p>{run.provider}/{run.model} · {run.usage.tool_calls} tool calls</p>{run.report.changed?.map((e, i) => <p key={i}>Changed: {e.summary} — {e.source}</p>)}{run.report.unchanged?.map((e, i) => <p key={i}>Unchanged: {e.summary}</p>)}{run.report.unverified?.map((e, i) => <p key={i}>Unverified: {e}</p>)}</article>)}{snapshot.activity.map(a => <p key={a.sequence}>#{a.sequence} {a.kind} · {a.created_at}</p>)}<Button onClick={() => snapshot.markRead(Math.max(0, ...snapshot.activity.map(a => a.sequence))).catch(e => setError(botLocalFailure(e).message))}>Mark displayed activity read</Button>{error && <BotEmployeeMessage role="alert">{error}</BotEmployeeMessage>}</details>
      {route.runId && <BotRunDetails key={route.runId} bot={bot} runId={route.runId} readOnly={unavailable} revision={revision} refresh={refresh} />}
    <details><summary>Operation evidence</summary><p>{snapshot.data?.mandate?.available ? 'A stored mandate is available. Each action still checks current authorization.' : 'No stored mandate is available.'}</p>{snapshot.error ? <p role="alert">Operation evidence is unavailable. Refresh before relying on these results.</p> : (snapshot.data?.operations || []).length ? (snapshot.data?.operations || []).map(operation => <p key={operation.operation_id}>{operation.summary} · {operation.status}</p>) : <p>No operation results yet.</p>}</details>
    <details><summary>Review inbox</summary>{reviews}</details>
    <details><summary>Goals</summary>{goals.error && <p role="alert">{goals.error}<Button onClick={goals.retry}>Retry goals</Button></p>}{goals.data && <BotGoals bot={bot} goals={goals.data.goals} readOnly={unavailable} onChange={refresh} />}</details>
    <details><summary>Schedule & triggers</summary>{version.data && <BotSchedule bot={bot} version={version.data} readOnly={unavailable} onChange={refresh} />}</details>
    <details><summary>Memory & charter</summary><BotMemoryCharterPanel bot={bot} readOnly={unavailable} onChange={refresh} /></details>
    <details><summary>Configuration</summary><BotConfigurationPanel bot={bot} readOnly={unavailable} onChange={refresh} />{!readOnly && <BotTemplatePublication />}</details>
  </div>
}
function BotRunDetails({ bot, runId, readOnly, revision, refresh }: { bot: api.Bot; runId: string; readOnly: boolean; revision: number; refresh: () => void }) {
  const run = useResource(s => api.getBotRun(bot.id, runId, bot.scope, s), `${bot.id}/${runId}`, revision)
  return <section aria-label="Selected run" className="space-y-2">
    <h3>Run {runId}</h3>
    {run.error && <p role="alert">{run.error}<Button onClick={run.retry}>Retry run</Button></p>}
    {!run.data && !run.error && <p role="status">Loading run…</p>}
    {run.data && <><BotStopRun bot={bot} run={run.data} disabled={readOnly || !!run.error} onChange={refresh} />
      <p>{run.data.report?.summary || 'No report yet.'}</p>
      <BotQualityDetails quality={run.data.report?.quality} unavailable={!!run.error} />
      <p className="text-sm text-muted-foreground">Execution trace — may include unassessed drafts, not final deliverables.</p>
      {run.data.session_id && <div className="h-[60dvh] flex flex-col"><StudioChat key={run.data.session_id} theme="dark" initialSessionId={run.data.session_id} hideSidebar botThread={{ botId: bot.id, scope: bot.scope, readOnly: true, onSend: async () => { throw new Error('Run traces are read-only') } }} /></div>}
    </>}
  </section>
}
