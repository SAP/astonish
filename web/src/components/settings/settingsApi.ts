// Shared API and UI utilities for settings components
import type { CSSProperties } from 'react'
import { teamFetch } from '../../api/teamContext'

// --- Types ---

export interface FullConfig {
  [sectionKey: string]: Record<string, unknown>
}

export interface ProviderInfo {
  name: string
  type: string
  display_name: string
  configured: boolean
  fields: Record<string, string>
}

export interface SettingsData {
  general: {
    default_provider: string
    default_model: string
    web_search_tool: string
    web_extract_tool: string
    timezone: string
  }
  providers: ProviderInfo[]
}

export interface MCPServerConfig {
  command?: string
  args?: string[]
  env?: Record<string, string>
  transport?: string
  url?: string
  enabled?: boolean
}

export interface MCPConfigData {
  mcpServers: Record<string, MCPServerConfig>
}

export interface MCPServerStatusEntry {
  name: string
  status: string
  error?: string | null
  tool_count?: number
}

export interface WebTool {
  source: string
  name: string
}

export interface WebCapableTools {
  webSearch: WebTool[]
  webExtract: WebTool[]
}

export interface StandardServerEnvVar {
  name: string
  required: boolean
}

export interface StandardServer {
  id: string
  displayName: string
  isDefault: boolean
  installed: boolean
  envVars: StandardServerEnvVar[]
  capabilities: {
    webSearch: boolean
    webExtract: boolean
  }
}

export interface TapEntry {
  name: string
  url: string
}

export interface UpdateInfo {
  version: string
  [key: string]: unknown
}

export interface ProviderFieldDef {
  key: string
  label: string
  type: string
}

// --- API Functions ---

export const fetchFullConfig = async (): Promise<FullConfig> => {
  const res = await teamFetch('/api/settings/full')
  if (!res.ok) throw new Error('Failed to fetch config')
  return res.json()
}

export const saveFullConfigSection = async (sectionKey: string, data: Record<string, unknown>): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/settings/full', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ [sectionKey]: data })
  })
  if (!res.ok) throw new Error('Failed to save settings')
  return res.json()
}

export const fetchSettings = async (): Promise<SettingsData> => {
  const res = await teamFetch('/api/settings/config')
  if (!res.ok) throw new Error('Failed to fetch settings')
  return res.json()
}

export const saveSettings = async (data: Record<string, unknown>): Promise<unknown> => {
  const res = await teamFetch('/api/settings/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  if (!res.ok) throw new Error('Failed to save settings')
  return res.json()
}

export const replaceAllProviders = async (providers: Record<string, unknown>[]): Promise<unknown> => {
  const res = await teamFetch('/api/settings/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ providers: { '__replace_all__': { '__array__': JSON.stringify(providers) } } })
  })
  if (!res.ok) throw new Error('Failed to replace providers')
  return res.json()
}

export const fetchMCPConfig = async (teamSlug?: string, scope?: string): Promise<MCPConfigData> => {
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const url = effectiveScope ? `/api/settings/mcp?scope=${effectiveScope}` : '/api/settings/mcp'
  const res = await teamFetch(url, undefined, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to fetch MCP config')
  return res.json()
}

export const saveMCPConfig = async (data: Record<string, unknown>, teamSlug?: string, scope?: string): Promise<unknown> => {
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const url = effectiveScope ? `/api/settings/mcp?scope=${effectiveScope}` : '/api/settings/mcp'
  const res = await teamFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to save MCP config')
  return res.json()
}

export const fetchProviderModels = async (providerId: string): Promise<{ models: string[] }> => {
  const res = await teamFetch(`/api/providers/${providerId}/models`)
  if (!res.ok) throw new Error('Failed to fetch models')
  return res.json()
}

// Fetch tools that have 'websearch' or 'webextract' in their name
export const fetchWebCapableTools = async (): Promise<WebCapableTools> => {
  const res = await teamFetch('/api/tools/web-capable')
  if (!res.ok) throw new Error('Failed to fetch web-capable tools')
  return res.json()
}

// Taps API functions
export const fetchTaps = async (teamSlug?: string): Promise<{ taps: TapEntry[] }> => {
  const res = await teamFetch('/api/taps', undefined, teamSlug)
  if (!res.ok) throw new Error('Failed to fetch taps')
  return res.json()
}

export const addTap = async (url: string, alias: string = '', teamSlug?: string): Promise<unknown> => {
  const res = await teamFetch('/api/taps', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url, alias })
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to add tap')
  }
  return res.json()
}

export const removeTap = async (name: string, teamSlug?: string): Promise<unknown> => {
  const res = await teamFetch(`/api/taps/${encodeURIComponent(name)}`, {
    method: 'DELETE'
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to remove tap')
  }
  return res.json()
}

// Fetch MCP server status
export const fetchMCPStatus = async (teamSlug?: string, scope?: string): Promise<{ servers: MCPServerStatusEntry[] }> => {
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const url = effectiveScope ? `/api/mcp/status?scope=${effectiveScope}` : '/api/mcp/status'
  const res = await teamFetch(url, undefined, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to fetch MCP status')
  return res.json()
}

// Toggle MCP server enabled/disabled
export const toggleMCPServer = async (name: string, enabled: boolean, teamSlug?: string, scope?: string): Promise<unknown> => {
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const url = effectiveScope
    ? `/api/mcp/servers/${encodeURIComponent(name)}?scope=${effectiveScope}`
    : `/api/mcp/servers/${encodeURIComponent(name)}`
  const res = await teamFetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled })
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) throw new Error('Failed to toggle server')
  return res.json()
}

export const refreshMCPServer = async (serverName: string, teamSlug?: string, scope?: string): Promise<unknown> => {
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const url = effectiveScope
    ? `/api/mcp/${encodeURIComponent(serverName)}/refresh?scope=${effectiveScope}`
    : `/api/mcp/${encodeURIComponent(serverName)}/refresh`
  const res = await teamFetch(url, {
    method: 'POST'
  }, scope === 'platform' ? undefined : teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to refresh server')
  }
  return res.json()
}

// --- Provider Settings (multi-level: platform / org / team) ---

export interface LevelProviderData {
  providers: Record<string, Record<string, string>> | null
  default_provider: string
  default_model: string
}

export const fetchPlatformProviders = async (): Promise<LevelProviderData> => {
  const res = await teamFetch('/api/settings/platform/providers')
  if (!res.ok) throw new Error('Failed to fetch platform providers')
  return res.json()
}

export const savePlatformProviders = async (data: {
  providers?: Record<string, Record<string, string>>
  default_provider?: string
  default_model?: string
}): Promise<unknown> => {
  const res = await teamFetch('/api/settings/platform/providers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  if (!res.ok) throw new Error('Failed to save platform providers')
  return res.json()
}

export const fetchOrgProviders = async (): Promise<LevelProviderData> => {
  const res = await teamFetch('/api/settings/org/providers')
  if (!res.ok) throw new Error('Failed to fetch org providers')
  return res.json()
}

export const saveOrgProviders = async (data: {
  providers?: Record<string, Record<string, string>>
  default_provider?: string
  default_model?: string
}): Promise<unknown> => {
  const res = await teamFetch('/api/settings/org/providers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  if (!res.ok) throw new Error('Failed to save org providers')
  return res.json()
}

export const fetchTeamProviders = async (): Promise<LevelProviderData> => {
  const res = await teamFetch('/api/settings/team/providers')
  if (!res.ok) throw new Error('Failed to fetch team providers')
  return res.json()
}

export const fetchEffectiveProviders = async (): Promise<LevelProviderData> => {
  const res = await teamFetch('/api/settings/providers/effective')
  if (!res.ok) throw new Error('Failed to fetch effective providers')
  return res.json()
}

export const deleteProviderAtLevel = async (level: string, name: string): Promise<unknown> => {
  const res = await teamFetch(`/api/settings/${level}/providers/${encodeURIComponent(name)}`, {
    method: 'DELETE'
  })
  if (!res.ok) throw new Error('Failed to delete provider')
  return res.json()
}

export interface ProviderTestResult {
  success: boolean
  error?: string
  model_count?: number
  models?: string[]
}

export const testProviderConnection = async (type: string, params: Record<string, string>): Promise<ProviderTestResult> => {
  const res = await teamFetch('/api/settings/providers/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ type, params })
  })
  if (!res.ok) throw new Error('Failed to test provider connection')
  return res.json()
}

// --- Common Styles ---
// Surface hierarchy (match Credentials / Image #1):
//   page → work-background
//   section card → bg-card / --bg-secondary (elevated)
//   nested row / tile → --bg-tertiary or inset bg-background
//   primary CTA → --brand / primary

/** Section cards in Settings (Chat, Memory, MCP wrappers, …). */
export const settingsCardClass =
  'border-border bg-card text-card-foreground shadow-sm'

/** Nested rows / tiles inside a settings card (toggle rows, list items). */
export const settingsNestedClass =
  'rounded-xl border border-border bg-background'

/** Inherited / read-only rows (platform MCP, etc.) — visible, not dimmed black. */
export const settingsInheritedStyle: CSSProperties = {
  background: 'var(--card)',
  border: '1px solid var(--border)',
}

export const inputClass: string = 'w-full rounded-md border px-3 py-2 text-sm shadow-xs outline-none transition-[color,box-shadow] focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50'
export const inputStyle: CSSProperties = {
  background: 'var(--input-bg, var(--background))',
  borderColor: 'var(--border)',
  color: 'var(--foreground)'
}

export const labelStyle: CSSProperties = { color: 'var(--foreground)' }
export const hintStyle: CSSProperties = { color: 'var(--muted-foreground)' }
export const sectionBorderStyle: CSSProperties = { borderColor: 'var(--border)' }

export const saveButtonStyle: CSSProperties = {
  background: 'var(--brand, var(--primary))',
  color: 'var(--brand-foreground, var(--primary-foreground))'
}
