/**
 * API client for platform-mode endpoints.
 * Covers teams, app sharing, memories, and audit queries.
 * All endpoints require authentication (cookie-based JWT).
 *
 * Team-scoped requests use teamFetch() which injects the X-Astonish-Team
 * header so the backend resolves the correct team context.
 */

import { teamFetch } from './teamContext'

/**
 * Throws a meaningful error from a non-OK Response. Tries to parse the
 * backend JSON body for a `message` or `error` field; falls back to
 * `<fallback> (HTTP <status>)` if the body isn't JSON or is empty.
 *
 * Use this everywhere we surface an HTTP failure to the user — never
 * throw a hard-coded generic string, because it makes server-side
 * problems undebuggable from the browser console.
 */
async function throwBackendError(res: Response, fallback: string): Promise<never> {
  let detail = ''
  try {
    const body = await res.json()
    detail = body?.message || body?.error || ''
  } catch {
    // ignore — body wasn't JSON
  }
  if (!detail) detail = res.statusText || ''
  const suffix = detail ? `: ${detail}` : ''
  throw new Error(`${fallback}${suffix} (HTTP ${res.status})`)
}

// --------------------------------------------------------------------------
// Teams
// --------------------------------------------------------------------------

export interface Team {
  slug: string
  name: string
  description: string
  created_at: string
  member_count?: number
}

export interface TeamMember {
  user_id: string
  email: string
  display_name: string
  role: string
  joined_at: string
}

export interface OrgInfo {
  id: string
  name: string
  slug: string
}

export async function fetchTeams(): Promise<Team[]> {
  const res = await teamFetch('/api/teams')
  if (!res.ok) await throwBackendError(res, 'Failed to fetch teams')
  const data = await res.json()
  return data.teams || []
}

export async function createTeam(name: string, slug: string, description?: string): Promise<Team> {
  const res = await teamFetch('/api/teams', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, slug, description: description || '' }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to create team')
  }
  return res.json()
}

export async function deleteTeam(slug: string): Promise<void> {
  const res = await teamFetch(`/api/teams/${slug}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to delete team')
  }
}

export async function renameTeam(slug: string, name: string): Promise<Team> {
  const res = await teamFetch(`/api/teams/${slug}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to rename team')
  }
  return res.json()
}

export interface TeamMembersResponse {
  members: TeamMember[]
  callerRole: string // 'org_admin' | 'admin' | 'member' | 'viewer'
}

export async function fetchTeamMembers(slug: string): Promise<TeamMembersResponse> {
  const res = await teamFetch(`/api/teams/${slug}/members`)
  if (!res.ok) await throwBackendError(res, 'Failed to fetch team members')
  const data = await res.json()
  return { members: data.members || [], callerRole: data.callerRole || '' }
}

export async function addTeamMember(slug: string, email: string, role?: string, sendNotify?: boolean): Promise<void> {
  const res = await teamFetch(`/api/teams/${slug}/members`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, role: role || 'member', send_notify: sendNotify ?? true }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to add team member')
  }
}

export async function removeTeamMember(slug: string, userID: string): Promise<void> {
  const res = await teamFetch(`/api/teams/${slug}/members/${userID}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to remove team member')
  }
}

export async function setTeamMemberRole(slug: string, userID: string, role: string): Promise<void> {
  const res = await teamFetch(`/api/teams/${slug}/members/${userID}/role`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to update member role')
  }
}

export async function fetchOrg(): Promise<OrgInfo> {
  const res = await teamFetch('/api/org')
  if (!res.ok) await throwBackendError(res, 'Failed to fetch org info')
  return res.json()
}

export async function updateOrg(name: string): Promise<OrgInfo> {
  const res = await teamFetch('/api/org', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to update organization')
  }
  return res.json()
}

// --------------------------------------------------------------------------
// App Sharing
// --------------------------------------------------------------------------

export interface AppItem {
  name: string
  description: string
  version: number
  updatedAt: string
}

export async function publishAppToTeam(slug: string): Promise<{ slug: string }> {
  const res = await teamFetch('/api/apps/publish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to publish app')
  }
  return res.json()
}

export async function forkApp(slug: string, source: 'team' | 'org'): Promise<{ slug: string }> {
  const res = await teamFetch('/api/apps/fork', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug, source }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to fork app')
  }
  return res.json()
}

export async function promoteAppToOrg(slug: string, teamSlug: string): Promise<{ slug: string }> {
  const res = await teamFetch('/api/apps/promote', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug, team_slug: teamSlug }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to promote app')
  }
  return res.json()
}

export async function fetchOrgApps(): Promise<AppItem[]> {
  const res = await teamFetch('/api/apps/org')
  if (!res.ok) await throwBackendError(res, 'Failed to fetch org apps')
  const data = await res.json()
  return data.apps || []
}

export async function deleteOrgApp(name: string): Promise<void> {
  const res = await teamFetch(`/api/apps/org/${name}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to delete org app')
  }
}

// --------------------------------------------------------------------------
// Memories (Knowledge Sharing)
// --------------------------------------------------------------------------

export interface MemoryEntry {
  id: string
  snippet: string
  category: string
  scope: string
  score?: number
  created_at?: string
  created_by?: string
  session_id?: string
}

export interface MemoryMapFlag {
  type: string
  severity: string
  description: string
}

export interface MemoryMapGroup {
  key: string
  title: string
  memory_count: number
  scopes: string[]
  categories: string[]
  session_ids?: string[]
  created_by?: string[]
  flags?: MemoryMapFlag[]
  has_scenario_card?: boolean
  scenario_card_id?: string
  representative: MemoryEntry
  memories: MemoryEntry[]
}

export interface ScenarioCard {
  canonical_key: string
  scope?: string
  title: string
  aliases?: string[]
  category?: string
  facts?: string[]
  recommended_recipe: string[]
  conditions?: string[]
  cautions_or_conditional_failures?: string[]
  verification?: string[]
  source_memory_ids?: string[]
  source_session_ids?: string[]
  status: string
  confidence?: number
  last_verified_at?: string
}

export interface MemoryConsolidationPreview {
  card: ScenarioCard
  content: string
  sources: string[]
}

export interface MemoryConsolidationApplyResponse {
  applied: boolean
  scope: string
  action: string
  existing_id?: string
  card: ScenarioCard
  deleted_sources?: number
}

export interface MemoryRecommendation {
  id: string
  type: string
  severity: string
  title: string
  description: string
  target_scope: 'personal' | 'team' | 'org'
  group_key: string
  memory_ids: string[]
  flags?: MemoryMapFlag[]
  card: ScenarioCard
}

export interface MemoryHealthResponse {
  evaluated_at: string
  expires_at: string
  generated: boolean
  recommendation_count: number
  recommendations: MemoryRecommendation[]
  map: MemoryMapResponse
}

export interface MemoryMapResponse {
  groups: MemoryMapGroup[]
  stats: {
    total_memories: number
    group_count: number
    duplicate_risk_count: number
    scattered_topic_count: number
    transient_risk_count: number
    trial_error_risk_count: number
  }
}

export async function searchMemories(query: string, limit?: number, teamSlug?: string): Promise<MemoryEntry[]> {
  const res = await teamFetch('/api/memories/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, max_results: limit || 20 }),
  }, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to search memories')
  const data = await res.json()
  return data.results || []
}

export async function fetchMemoryMap(limit?: number, teamSlug?: string): Promise<MemoryMapResponse> {
  const suffix = limit ? `?limit=${encodeURIComponent(String(limit))}` : ''
  const res = await teamFetch(`/api/memories/map${suffix}`, undefined, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to fetch memory map')
  return res.json()
}

export async function fetchMemoryHealth(limit?: number, refresh?: boolean, teamSlug?: string): Promise<MemoryHealthResponse> {
  const params = new URLSearchParams()
  if (limit) params.set('limit', String(limit))
  if (refresh) params.set('refresh', 'true')
  const suffix = params.toString() ? `?${params.toString()}` : ''
  const res = await teamFetch(`/api/memories/health${suffix}`, undefined, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to analyze memory health')
  return res.json()
}

export async function previewMemoryConsolidation(key: string, targetScope: string, memoryIds?: string[], teamSlug?: string): Promise<MemoryConsolidationPreview> {
  const res = await teamFetch('/api/memories/consolidate/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key, target_scope: targetScope, memory_ids: memoryIds || [] }),
  }, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to draft consolidated memory')
  return res.json()
}

export async function applyMemoryConsolidation(card: ScenarioCard, targetScope: string, teamSlug?: string): Promise<MemoryConsolidationApplyResponse> {
  const res = await teamFetch('/api/memories/consolidate/apply', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ card, target_scope: targetScope }),
  }, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to save consolidated memory')
  return res.json()
}

export async function saveTeamMemory(snippet: string, category?: string, teamSlug?: string): Promise<{ id: string }> {
  const res = await teamFetch('/api/memories/team', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: snippet, category: category || 'general' }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to save team memory')
  }
  return res.json()
}

export async function savePersonalMemory(snippet: string, category?: string, teamSlug?: string): Promise<{ id: string }> {
  const res = await teamFetch('/api/memories/personal', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: snippet, category: category || 'general' }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to save personal memory')
  }
  return res.json()
}

export async function listTeamMemories(teamSlug?: string): Promise<MemoryEntry[]> {
  const res = await teamFetch('/api/memories/team', undefined, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to list team memories')
  const data = await res.json()
  return data.results || []
}

export async function listOrgMemories(teamSlug?: string): Promise<MemoryEntry[]> {
  const res = await teamFetch('/api/memories/org', undefined, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to list org memories')
  const data = await res.json()
  return data.results || []
}

export async function deleteTeamMemory(id: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch(`/api/memories/team/${id}`, { method: 'DELETE' }, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to delete team memory')
}

export async function deleteOrgMemory(id: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch(`/api/memories/org/${id}`, { method: 'DELETE' }, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to delete org memory')
}

export async function promoteMemoryToOrg(id: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch('/api/memories/promote', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ memory_id: id, team_slug: teamSlug || '' }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to promote memory')
  }
}

export async function listPersonalMemories(teamSlug?: string): Promise<MemoryEntry[]> {
  const res = await teamFetch('/api/memories/personal', undefined, teamSlug)
  if (!res.ok) await throwBackendError(res, 'Failed to list personal memories')
  const data = await res.json()
  return data.results || []
}

export async function deletePersonalMemory(id: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch(`/api/memories/personal/${id}`, { method: 'DELETE' }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to delete personal memory')
  }
}

export async function promotePersonalToTeam(id: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch('/api/memories/promote-to-team', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ memory_id: id }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to promote memory to team')
  }
}

export async function updateMemory(scope: string, id: string, content: string, category?: string, teamSlug?: string): Promise<void> {
  const res = await teamFetch(`/api/memories/${scope}/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, category }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to update memory')
  }
}

export async function saveOrgMemory(snippet: string, category?: string, teamSlug?: string): Promise<{ id: string }> {
  const res = await teamFetch('/api/memories/org', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: snippet, category: category || 'general' }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to save org memory')
  }
  return res.json()
}

// --------------------------------------------------------------------------
// Session Memories
// --------------------------------------------------------------------------

export interface SessionMemoriesResponse {
  session_id: string
  memories: MemoryEntry[]
  count: number
}

export interface ExtractionEntry {
  category: string
  content: string
}

export interface ExtractionResponse {
  session_id: string
  original_count: number
  entries: ExtractionEntry[]
  applied: boolean
}

export async function listSessionMemories(sessionId: string, teamSlug?: string): Promise<SessionMemoriesResponse> {
  const res = await teamFetch(`/api/memories/session/${sessionId}`, {}, teamSlug)
  if (!res.ok) {
    await throwBackendError(res, 'Failed to list session memories')
  }
  return res.json()
}

export async function extractSessionMemories(sessionId: string, dryRun = true, teamSlug?: string): Promise<ExtractionResponse> {
  const res = await teamFetch(`/api/memories/session/${sessionId}/extract`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dry_run: dryRun }),
  }, teamSlug)
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(err.message || err.error || 'Failed to extract session memories')
  }
  return res.json()
}

// --------------------------------------------------------------------------
// Audit Logs
// --------------------------------------------------------------------------

export interface AuditEntry {
  id: number
  timestamp: string
  user_id: string
  team_id: string
  action: string
  resource: string
  detail: Record<string, unknown>
  ip_address: string
  session_id: string
}

export interface AuditFilter {
  user_id?: string
  action?: string
  resource?: string
  since?: string
  until?: string
  limit?: number
  offset?: number
}

export async function queryAuditLogs(filter: AuditFilter = {}): Promise<{ entries: AuditEntry[]; count: number }> {
  const params = new URLSearchParams()
  if (filter.user_id) params.set('user_id', filter.user_id)
  if (filter.action) params.set('action', filter.action)
  if (filter.resource) params.set('resource', filter.resource)
  if (filter.since) params.set('since', filter.since)
  if (filter.until) params.set('until', filter.until)
  if (filter.limit) params.set('limit', String(filter.limit))
  if (filter.offset) params.set('offset', String(filter.offset))

  const res = await teamFetch(`/api/audit?${params.toString()}`)
  if (!res.ok) await throwBackendError(res, 'Failed to query audit logs')
  return res.json()
}

// --------------------------------------------------------------------------
// Platform Setup (Deployment Mode)
// --------------------------------------------------------------------------

export interface PlatformInitParams {
  host: string
  port: number
  user: string
  password: string
  ssl_mode: string
  org_name: string
  org_slug: string
}

export interface SQLitePlatformInitParams {
  data_dir?: string
  org_name: string
  org_slug: string
  admin_email: string
  admin_name?: string
  admin_password: string
}

export interface PlatformInitResult {
  success: boolean
  message: string
  restart_required: boolean
  error?: string
}

export interface DeploymentModeInfo {
  mode: 'personal' | 'platform' | 'sqlite'
  configured: boolean
}

export async function initializePlatform(params: PlatformInitParams): Promise<PlatformInitResult> {
  const res = await fetch('/api/platform/init', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  const data = await res.json()
  if (!res.ok) {
    throw new Error(data.error || 'Failed to initialize platform')
  }
  return data
}

export async function initializeSQLitePlatform(params: SQLitePlatformInitParams): Promise<PlatformInitResult> {
  const res = await fetch('/api/platform/init/sqlite', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  const data = await res.json()
  if (!res.ok) {
    throw new Error(data.error || 'Failed to initialize SQLite platform')
  }
  return data
}

export async function getDeploymentMode(): Promise<DeploymentModeInfo> {
  const res = await fetch('/api/platform/mode')
  if (!res.ok) {
    return { mode: 'personal', configured: false }
  }
  return res.json()
}

export async function getPlatformInitStatus(): Promise<{ configured: boolean; initialized: boolean }> {
  const res = await fetch('/api/platform/init/status')
  if (!res.ok) {
    return { configured: false, initialized: false }
  }
  return res.json()
}

// --------------------------------------------------------------------------
// User Management (Org Admin)
// --------------------------------------------------------------------------

export interface OrgUser {
  id: string
  email: string
  display_name: string
  status: string   // 'active' | 'disabled'
  role: string     // 'owner' | 'admin' | 'member'
  joined_at: string
  created_at: string
  has_oidc: boolean
}

export async function fetchOrgUsers(): Promise<OrgUser[]> {
  const res = await teamFetch('/api/admin/users')
  if (!res.ok) await throwBackendError(res, 'Failed to fetch users')
  const data = await res.json()
  return data.users || []
}

export async function setUserOrgRole(userId: string, role: string): Promise<void> {
  const res = await teamFetch(`/api/admin/users/${userId}/role`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || err.message || 'Failed to update role')
  }
}

export async function setUserStatus(userId: string, status: string): Promise<void> {
  const res = await teamFetch(`/api/admin/users/${userId}/status`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || err.message || 'Failed to update status')
  }
}

export async function deleteOrgUser(userId: string): Promise<void> {
  const res = await teamFetch(`/api/admin/users/${userId}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || err.message || 'Failed to remove user')
  }
}

export async function inviteUserToOrg(params: {
  email: string
  display_name: string
  role: string
  send_invite: boolean
}): Promise<{ user: OrgUser; created: boolean }> {
  const res = await teamFetch('/api/admin/users/invite', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || err.message || 'Failed to add user')
  }
  return res.json()
}

export async function resetUserPassword(userId: string, password: string): Promise<void> {
  const res = await teamFetch(`/api/admin/users/${userId}/password`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || err.message || 'Failed to reset password')
  }
}
