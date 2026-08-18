import { teamFetch } from './teamContext'

// --- Types ---

export interface A2AAgentListItem {
  name: string
  url: string
  credential_name?: string
  auth_type?: string
  enabled: boolean
  headers?: Record<string, string>
  timeout?: string
  scope: string
  has_card: boolean
  skill_count: number
  cached_skills?: any
}

export interface A2AAgentsListResponse {
  agents: A2AAgentListItem[]
  is_team_admin: boolean
  is_org_admin: boolean
}

export interface A2AAgentCreateRequest {
  name: string
  url: string
  credential_name?: string
  auth_type?: string
  enabled?: boolean
  headers?: Record<string, string>
  timeout?: string
}

export interface A2AAgentTestResult {
  status: 'ok' | 'error'
  message?: string
  skills?: any[]
}

// --- API Functions ---

export async function fetchA2AAgents(scope?: string, teamSlug?: string): Promise<A2AAgentsListResponse> {
  const url = scope ? `/api/a2a-agents?scope=${scope}` : '/api/a2a-agents'
  const res = await teamFetch(url, undefined, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to fetch A2A agents')
  return res.json()
}

export async function createA2AAgent(agent: A2AAgentCreateRequest, scope?: string, teamSlug?: string): Promise<{ status: string; name: string }> {
  const url = scope ? `/api/a2a-agents?scope=${scope}` : '/api/a2a-agents'
  const res = await teamFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(agent)
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to create A2A agent')
  return res.json()
}

export async function updateA2AAgent(name: string, agent: A2AAgentCreateRequest, scope?: string, teamSlug?: string): Promise<{ status: string; name: string }> {
  const base = `/api/a2a-agents/${encodeURIComponent(name)}`
  const url = scope ? `${base}?scope=${scope}` : base
  const res = await teamFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(agent)
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to update A2A agent')
  return res.json()
}

export async function deleteA2AAgent(name: string, scope?: string, teamSlug?: string): Promise<{ status: string }> {
  const base = `/api/a2a-agents/${encodeURIComponent(name)}`
  const url = scope ? `${base}?scope=${scope}` : base
  const res = await teamFetch(url, {
    method: 'DELETE'
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to delete A2A agent')
  return res.json()
}

export async function toggleA2AAgent(name: string, enabled: boolean, scope?: string, teamSlug?: string): Promise<{ status: string; enabled: boolean }> {
  const base = `/api/a2a-agents/${encodeURIComponent(name)}`
  const url = scope ? `${base}?scope=${scope}` : base
  const res = await teamFetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled })
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to toggle A2A agent')
  return res.json()
}

export async function refreshA2AAgent(name: string, scope?: string, teamSlug?: string): Promise<{ status: string; name: string; skill_count: number }> {
  const base = `/api/a2a-agents/${encodeURIComponent(name)}/refresh`
  const url = scope ? `${base}?scope=${scope}` : base
  const res = await teamFetch(url, {
    method: 'POST'
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to refresh A2A agent')
  return res.json()
}

export async function testA2AAgent(name: string, scope?: string, teamSlug?: string): Promise<A2AAgentTestResult> {
  const base = `/api/a2a-agents/${encodeURIComponent(name)}/test`
  const url = scope ? `${base}?scope=${scope}` : base
  const res = await teamFetch(url, {
    method: 'POST'
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to test A2A agent')
  return res.json()
}
