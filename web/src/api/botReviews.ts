import { BotAPIError, botURL, type Bot, type BotApproval, type BotCarryover, type BotGoal, type BotPersona, type BotRun, type BotScope, type BotTemplate, type BotTrigger, type BotVersion } from './bots'
import type { AttachmentPayload } from './studioChat'
import type { BotAdaptiveReview } from './botAdaptive'
import { teamFetch } from './teamContext'

export const ProposalStatus = { Proposed: 'proposed', Applied: 'applied', Rejected: 'rejected', Expired: 'expired', Stale: 'stale' } as const
export type ControlPayload = { reason: string } & (
  | { operation: 'responsibility'; responsibility: string }
  | { operation: 'persona'; persona: BotPersona }
  | { operation: 'configuration'; configuration: { pause?: boolean; autonomy: BotVersion['autonomy']; budget: BotVersion['budget']; delivery: BotVersion['delivery'] } }
  | { operation: 'goal_create' | 'goal_edit'; goal: { goal: BotGoal; expected_revision: number } }
  | { operation: 'memory_remember' | 'memory_correct' | 'memory_forget'; memory: { expected_fingerprint?: string; memory_id?: string; content?: string; category?: string; expected_revision: number } }
  | { operation: 'schedule_create' | 'schedule_edit' | 'schedule_pause' | 'schedule_remove'; schedule: { trigger: BotTrigger; expected_revision: number } }
  | { operation: 'task_once'; task_once: { instructions: string } }
  | { operation: 'activate' | 'resume' }
)
export interface BotControlProposal {
  id: string; bot_id: string; source_run_id: string; source_user_id: string; version: number; expected_revision: number
  digest: string; payload: ControlPayload; status: typeof ProposalStatus[keyof typeof ProposalStatus]
  created_at: string; expires_at: string; decided_at?: string; decided_by?: string; reconciliation_pending: boolean
}
export interface BotMessageReceipt { run: BotRun; home_session_id: string; user_event_id: string; history_cursor: number }
export const Continuation = { Queued: 'queued', Blocked: 'blocked_by_pause' } as const
export interface BotContinueReceipt { approval: BotApproval; run_id: string; continuation: typeof Continuation[keyof typeof Continuation] }
export interface SourceIntakeRequest { mode: 'chat' | 'template' | 'adaptive'; source_id: string; template_revision?: number; name: string; charter: string; carryover: BotCarryover; idempotency_key: string }
export interface SourceIntakeProposal { id: string; owner_id: string; scope: BotScope; request: SourceIntakeRequest; source_hash: string; source_updated_at: string; template?: BotTemplate; adaptive?: BotAdaptiveReview; digest: string; status: 'proposed' | 'applying' | 'applied' | 'rejected' | 'stale'; bot_id?: string; created_at: string; decided_at?: string }
async function request<T>(path: string, scope: BotScope, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await teamFetch(botURL(path, scope), { method: body === undefined ? 'GET' : 'POST', signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || 'Bot request failed')
  return response.json() as Promise<T>
}
const id = encodeURIComponent
export const decideBotOperation = (bot: Pick<Bot, 'id' | 'scope'>, operation: import('./botOperationResult').BotOperationView, approve: boolean) => request<import('./botOperationResult').BotOperationView>(`/${id(bot.id)}/operations/${id(operation.operation_id)}/decision`, bot.scope, { payload_digest: operation.exception?.payload_digest, expected_revision: operation.revision, expires_at: operation.exception?.expires_at, approve })
export const continueInteractive = (bot: Bot, runId: string, key: string, response?: { question_id: string; answer: string }) => request<BotRun>(`/${id(bot.id)}/runs/${id(runId)}/continue-interactive`, bot.scope, { expected_revision: bot.revision, idempotency_key: key, ...(response ? { response } : {}) })
export const sendBotMessage = (bot: Bot, instructions: string, key: string, attachments?: AttachmentPayload[]) => request<BotMessageReceipt>(`/${id(bot.id)}/messages`, bot.scope, { version: bot.version, instructions, idempotency_key: key, ...(attachments?.length ? { attachments } : {}) })
export const listControlProposals = async (bot: Bot, signal?: AbortSignal) => { const result = await request<{ proposals: BotControlProposal[] | null }>(`/${id(bot.id)}/proposals`, bot.scope, undefined, signal); return { proposals: result.proposals || [] } }
export const decideControlProposal = (bot: Bot, proposal: BotControlProposal, decision: 'confirm' | 'reject') => request<BotControlProposal>(`/${id(bot.id)}/proposals/${id(proposal.id)}/decision`, bot.scope, { digest: proposal.digest, decision })
export const approveAndContinue = (bot: Bot, approval: BotApproval, answer?: string) => request<BotContinueReceipt>(`/${id(bot.id)}/approvals/${id(approval.id)}/approve-and-continue`, bot.scope, { action_digest: approval.action_digest, ...(answer ? { response: { question_id: approval.interaction?.question_id, answer } } : {}) })
export const proposeSourceIntake = (scope: BotScope, body: SourceIntakeRequest) => request<SourceIntakeProposal>('/source-proposals', scope, body)
export const getSourceIntake = (scope: BotScope, proposalId: string, signal?: AbortSignal) => request<SourceIntakeProposal>(`/source-proposals/${id(proposalId)}`, scope, undefined, signal)
export const decideSourceIntake = (scope: BotScope, proposal: SourceIntakeProposal, decision: 'confirm' | 'reject', reviewedForTeam: boolean) => request<SourceIntakeProposal>(`/source-proposals/${id(proposal.id)}/decision`, scope, { digest: proposal.digest, decision, reviewed_for_team: reviewedForTeam })
