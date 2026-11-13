export type { BotOperationView } from './botOperationResult'
import type { AttachmentPayload } from './studioChat'
import { teamFetch } from './teamContext'

export type BotScope = 'personal' | 'team'
export const BotStatus = { Draft: 'draft', Active: 'active', Paused: 'paused', Stopped: 'stopped', Deleted: 'deleted' } as const
export type BotStatusValue = typeof BotStatus[keyof typeof BotStatus]
export interface BotHomeBinding {
  id: string; bot_id: string; scope: BotScope; backend: string; storage_key: string; revision: number
}
export interface Bot {
  id: string; scope: BotScope; owner_id: string; name: string; status: BotStatusValue
  revision: number; version: number; home_session_id: string; home: BotHomeBinding | null
  created_at: string; updated_at: string; restore_until?: string; permanent_delete?: boolean
}
export interface BotPersona { avatar?: string; language: string; timezone: string; soul: string; identity: string; user: string }
export interface BotTrigger {
  id: string; kind: 'manual' | 'cron' | 'hook' | 'webhook' | 'channel' | 'goal_deadline'
  skill_id?: string; invocation?: string
  instructions: string; enabled: boolean; catch_up_once: boolean; cron?: string; timezone?: string; hook_status?: string
}
export interface BotVersion {
  bot_id: string; version: number; charter: string; persona: BotPersona
  autonomy: { level: string; can_contact_people: boolean; can_publish: boolean; max_spend_per_run_minor: number; max_spend_per_month_minor: number }
  budget: { max_runtime_seconds: number; max_tool_calls?: number; max_tokens?: number; max_runs_per_day?: number }
  delivery: { notify: string; preferences: string; bindings: { id: string; channel: string; chat_id: string }[] | null }
  triggers: BotTrigger[] | null
}
export interface BotEvidence { kind: 'observation' | 'owner_confirmation' | 'inference'; summary: string; source: string; observed_at: string; actor_id: string }
export const BotQualityOutcome = { Preparing: 'preparing', Producing: 'producing', CandidateReady: 'candidate_ready', Reviewing: 'reviewing', RepairReady: 'repair_ready', Repairing: 'repairing', Accepted: 'accepted', Blocked: 'blocked', Interrupted: 'interrupted' } as const
export const BotCriterionVerdict = { Satisfied: 'satisfied', Failed: 'failed', Unverified: 'unverified' } as const
export const BotAssessmentMethod = { Mechanical: 'mechanical_observation', Model: 'model_assessment' } as const
export interface BotCriterionAssessment {
  criterion_id: string
  verdict: typeof BotCriterionVerdict[keyof typeof BotCriterionVerdict]
  method: typeof BotAssessmentMethod[keyof typeof BotAssessmentMethod]
  evidence_refs: string[] | null
  explanation: string
}
export interface BotQualityReport {
  schema_version: number; work_revision: number; requirements_digest: string; candidate_digest?: string
  outcome: typeof BotQualityOutcome[keyof typeof BotQualityOutcome]
  criteria?: BotCriterionAssessment[] | null; repairs: number; limitations?: string[] | null
}
export const BotWorkProtocol = { Agent: 'agent_led_v1' } as const
export const WorkPhase = { Working: 'working', Finished: 'finished', Blocked: 'blocked', Interrupted: 'interrupted' } as const
export const WorkDisposition = { Answered: 'answered', Complete: 'complete', Partial: 'partial', Blocked: 'blocked' } as const
export const WorkItemStatus = { Pending: 'pending', Working: 'in_progress', Complete: 'completed', Blocked: 'blocked', Cancelled: 'cancelled' } as const
export const WorkCheckStatus = { Returned: 'returned', Failed: 'failed', Interrupted: 'interrupted' } as const
export const WorkCheckKind = { Feedback: 'model_feedback', Observation: 'mechanical_observation' } as const
export const WorkContinuation = { Owner: 'owner_request', Schedule: 'existing_schedule', Approval: 'existing_approval' } as const
export interface BotWorkProjection {
  schema_version: number; protocol: string; bot_id: string; run_id: string; work_revision: number
  phase: typeof WorkPhase[keyof typeof WorkPhase]; disposition?: typeof WorkDisposition[keyof typeof WorkDisposition]; candidate_digest?: string
  items?: { id: string; summary: string; status: typeof WorkItemStatus[keyof typeof WorkItemStatus]; reason?: string; evidence_refs?: string[] }[]
  remaining?: { id: string; source_run_id: string; work_item_id?: string; summary: string; reason: string; continuation: typeof WorkContinuation[keyof typeof WorkContinuation]; continuation_id?: string }[]
  checks?: { id: string; call_id: string; kind: typeof WorkCheckKind[keyof typeof WorkCheckKind]; status: typeof WorkCheckStatus[keyof typeof WorkCheckStatus]; judgment: 'supported' | 'needs_work' | 'unverified'; summary: string; draft_digest: string; evidence_refs?: string[]; observed_at: string }[]
}
export function botWorkSummary(work?: BotWorkProjection): string {
  if (!work) return 'No work result yet'
  if (work.schema_version !== 1 || work.protocol !== BotWorkProtocol.Agent) return 'Work details unavailable'
  if (work.disposition === WorkDisposition.Complete) return 'Completed by agent'
  if (work.disposition === WorkDisposition.Answered) return 'Answer provided'
  if (work.disposition === WorkDisposition.Partial) return 'Partial answer'
  if (work.disposition === WorkDisposition.Blocked) return 'Blocked'
  if (work.phase === WorkPhase.Interrupted) return 'Interrupted'
  if (work.phase === WorkPhase.Blocked) return 'Blocked'
  return work.phase === WorkPhase.Working ? 'Work in progress' : 'Work details unavailable'
}
export interface BotReport {
  summary: string; changed: BotEvidence[] | null; unchanged: BotEvidence[] | null; unverified: string[] | null; next_run_at?: string
  quality?: BotQualityReport
  skill?: import('./botSkills').BotSkillReceipt
}
export function botQualitySummary(q?: BotQualityReport): string {
  if (!q) return 'Not assessed'
  if (q.schema_version !== 1) return 'Assessment unavailable'
  if (q.outcome !== BotQualityOutcome.Accepted || !q.criteria?.length || q.limitations?.length || q.criteria.some(c => c.verdict !== BotCriterionVerdict.Satisfied || !c.evidence_refs?.length || !Object.values(BotAssessmentMethod).includes(c.method))) return 'Requirements remain unmet or unverified'
  return `Requirements checked · ${q.repairs} correction attempts. Model assessments are judgments, not guarantees.`
}
export interface BotRun {
  id: string; bot_id: string; session_id: string; version: number; trigger: string
  status: 'queued' | 'running' | 'waiting_approval' | 'interrupted' | 'succeeded' | 'partial' | 'failed' | 'skipped' | 'cancelled'
  created_at: string; started_at?: string; finished_at?: string; provider: string; model: string
  work?: BotWorkProjection
  report: BotReport
  usage: { tool_calls: number; input_tokens: number; output_tokens: number; cost_micros?: number }
}
export interface BotApproval {
  id: string; run_id: string; version: number; action_digest: string
  action: Record<string, unknown>; status: 'pending' | 'approved' | 'denied' | 'expired' | 'revoked' | 'consumed'; expires_at: string
  interaction?: { kind: 'chat_question'; question_id: string; payload: import('./questions').Question }
}
export interface BotGoal { id: string; revision: number; title: string; description: string; completion_criteria: string; status: 'active' | 'completed'; due_at?: string; evidence: BotEvidence[] | null }
export interface BotMemory { id?: string; snippet: string; path: string; category?: string; session_id?: string; created_at?: string }
export interface BotActivity { sequence: number; kind: string; run_id?: string; created_at: string; payload: unknown }
export interface BotTemplate { id: string; revision: number; definition: { schema_version: number; name: string; charter: string; language: string; suggested_goals: { title: string; description: string; completion_criteria: string }[] | null; suggested_schedules: { cron: string; timezone: string; instructions: string }[] | null } }
export interface BotDraft { name: string; start_from_title?: boolean; idempotency_key: string; definition?: { charter: string; persona?: BotPersona } }
export interface BotCarryover { summary: string; memories: { content: string; category: string }[]; goals: { title: string; completion_criteria: string }[]; artifacts: { title: string; content: string }[] }
export interface BotPromotion { name: string; charter: string; idempotency_key: string; reviewed: boolean; reviewed_for_team: boolean; carryover: BotCarryover }
export class BotAPIError extends Error { constructor(public status: number, message: string) { super(message) } }
const segment = encodeURIComponent
export function botURL(path = '', scope: BotScope = 'personal', query: Record<string, string> = {}) {
  return `/api/studio/bots${path}?${new URLSearchParams({ ...query, scope })}`
}
async function request<T>(path: string, scope: BotScope, method = 'GET', body?: unknown, signal?: AbortSignal, query?: Record<string, string>): Promise<T> {
  const response = await teamFetch(botURL(path, scope, query), { method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Bot request failed (${response.status})`)
  if (response.status === 204 || response.headers.get('Content-Length') === '0') return undefined as T
  const text = await response.text()
  return text ? JSON.parse(text) as T : undefined as T
}
export const listBots = (scope: BotScope, signal?: AbortSignal) => request<{ bots: Bot[] }>('', scope, 'GET', undefined, signal)
export const getBot = (id: string, scope: BotScope, signal?: AbortSignal) => request<Bot>(`/${segment(id)}`, scope, 'GET', undefined, signal)
export const getBotVersion = (id: string, version: number, scope: BotScope, signal?: AbortSignal) => request<BotVersion>(`/${segment(id)}/versions/${version}`, scope, 'GET', undefined, signal)
export interface BotHomeSetupRequest { expected_revision: number }
export const setupBotHome = (bot: Bot): Promise<Bot> => request<Bot>(`/${segment(bot.id)}/home`, bot.scope, 'POST', { expected_revision: bot.revision } satisfies BotHomeSetupRequest)
export const createBot = (draft: BotDraft) => request<Bot>('', 'personal', 'POST', draft)
export const promoteChatToBot = (session: string, draft: BotPromotion) => request<Bot>(`/promote-chat/${segment(session)}`, 'personal', 'POST', draft)
export const getBotTemplate = (id: string, signal?: AbortSignal) => request<BotTemplate>(`/templates/${segment(id)}`, 'team', 'GET', undefined, signal)
export const listBotTemplates = (signal?: AbortSignal) => request<{ templates: BotTemplate[] }>('/templates', 'team', 'GET', undefined, signal)
export const forkBotTemplate = (template: BotTemplate, name: string, key: string) => request<Bot>(`/templates/${segment(template.id)}/fork`, 'team', 'POST', { template_revision: template.revision, name, idempotency_key: key })
export const publishBotTemplate = (definition: BotTemplate['definition']) => request<BotTemplate>('/templates', 'team', 'POST', { expected_revision: 0, reviewed_for_team: true, definition })
export interface BotPermanentDeleteRequest { expected_revision: number; confirm: true }
export interface BotPermanentDeletePending { status: 'pending'; bot: Bot }
export async function permanentDeleteBot(bot: Bot, signal?: AbortSignal): Promise<BotPermanentDeletePending | undefined> {
  const response = await teamFetch(botURL(`/${segment(bot.id)}/permanent-delete`, bot.scope), { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: bot.revision, confirm: true } satisfies BotPermanentDeleteRequest) })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Bot request failed (${response.status})`)
  if (response.status === 204) return undefined
  const result = await response.json() as BotPermanentDeletePending
  if (response.status !== 202 || result.status !== 'pending' || result.bot?.id !== bot.id || result.bot?.scope !== bot.scope) throw new BotAPIError(response.status, 'Unexpected deletion response; completion has not been confirmed')
  return result
}
export const botLifecycle = (bot: Bot, action: 'activate' | 'pause' | 'stop' | 'restore' | 'delete', reason: string) => request<Bot>(`/${segment(bot.id)}${action === 'delete' ? '' : `/${action}`}`, bot.scope, action === 'delete' ? 'DELETE' : 'POST', { expected_revision: bot.revision, reason })
export const requestBotRun = (bot: Bot, instructions: string, key: string, attachments?: AttachmentPayload[]) => request<BotRun>(`/${segment(bot.id)}/runs`, bot.scope, 'POST', { version: bot.version, instructions, idempotency_key: key, ...(attachments?.length ? { attachments } : {}) })
export interface BotIntentRequest { version: number; instructions: string; idempotency_key: string }
export interface BotConversationRequest extends BotIntentRequest { attachments?: AttachmentPayload[] }
export const requestBotTestRun = (bot: Bot, instructions: string, key: string): Promise<BotRun> => request<BotRun>(`/${segment(bot.id)}/test-run`, bot.scope, 'POST', { version: bot.version, instructions, idempotency_key: key } satisfies BotIntentRequest)
export const requestBotConversation = (bot: Bot, instructions: string, key: string, attachments?: AttachmentPayload[]): Promise<BotRun> => request<BotRun>(`/${segment(bot.id)}/conversation`, bot.scope, 'POST', { version: bot.version, instructions, idempotency_key: key, ...(attachments?.length ? { attachments } : {}) } satisfies BotConversationRequest)
export const listBotRuns = (id: string, scope: BotScope, signal?: AbortSignal) => request<{ runs: BotRun[] }>(`/${segment(id)}/runs`, scope, 'GET', undefined, signal)
export const getBotRun = (id: string, run: string, scope: BotScope, signal?: AbortSignal) => request<BotRun>(`/${segment(id)}/runs/${segment(run)}`, scope, 'GET', undefined, signal)
export const listBotApprovals = (id: string, scope: BotScope, signal?: AbortSignal) => request<{ approvals: BotApproval[] }>(`/${segment(id)}/approvals`, scope, 'GET', undefined, signal)
export const decideBotApproval = (bot: Bot, approval: BotApproval, status: 'approved' | 'denied', answer?: string) => request<BotApproval>(`/${segment(bot.id)}/approvals/${segment(approval.id)}/decision`, bot.scope, 'POST', { action_digest: approval.action_digest, status, ...(answer ? { response: { question_id: approval.interaction?.question_id, answer } } : {}) })
export const resumeBotRun = (bot: Bot, approval: BotApproval) => request<void>(`/${segment(bot.id)}/runs/${segment(approval.run_id)}/resume`, bot.scope, 'POST', { approval_id: approval.id })
export const listBotGoals = (id: string, scope: BotScope, signal?: AbortSignal) => request<{ goals: BotGoal[] }>(`/${segment(id)}/goals`, scope, 'GET', undefined, signal)
export const saveBotGoal = (bot: Bot, goal: Pick<BotGoal, 'title' | 'description' | 'completion_criteria'>) => request<BotGoal>(`/${segment(bot.id)}/goals`, bot.scope, 'POST', { ...goal, expected_revision: 0 })
export const completeBotGoal = (bot: Bot, goal: BotGoal, summary: string, source: string) => request<BotGoal>(`/${segment(bot.id)}/goals/${segment(goal.id)}/complete`, bot.scope, 'POST', { expected_revision: goal.revision, evidence: { summary, source, observed_at: new Date().toISOString() } })
export const listBotMemories = (id: string, scope: BotScope, signal?: AbortSignal) => request<{ memories: BotMemory[] }>(`/${segment(id)}/memory`, scope, 'GET', undefined, signal)
export const listBotActivity = (id: string, scope: BotScope, signal?: AbortSignal) => request<{ activity: BotActivity[] }>(`/${segment(id)}/activity`, scope, 'GET', undefined, signal)
export const markBotRead = (bot: Bot, sequence: number) => request<void>(`/${segment(bot.id)}/read`, bot.scope, 'POST', { sequence })
export const updateBotCharter = (bot: Bot, charter: string, reason: string) => request<Bot>(`/${segment(bot.id)}/versions`, bot.scope, 'POST', { expected_revision: bot.revision, change_reason: reason, definition: { charter } })
export const saveBotTrigger = (bot: Bot, trigger: BotTrigger, reason: string) => request<Bot>(`/${segment(bot.id)}/triggers/${segment(trigger.id)}`, bot.scope, 'PUT', { expected_revision: bot.revision, reason, definition: trigger })
export const changeBotTrigger = (bot: Bot, trigger: BotTrigger, action: 'enable' | 'disable' | 'dry-run' | 'remove', reason: string) => request<Bot>(`/${segment(bot.id)}/triggers/${segment(trigger.id)}${action === 'remove' ? '' : `/${action}`}`, bot.scope, action === 'remove' ? 'DELETE' : 'POST', { expected_revision: bot.revision, reason })
