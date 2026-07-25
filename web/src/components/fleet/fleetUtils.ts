// ─── Local types for shapes the backend returns but the API layer types loosely ───

import type { ChatSession } from '../../api/studioChat'
import type { FleetPlanStatus, FleetThread } from '../../api/fleetChat'

/** ChatSession extended with fleet-specific fields the backend may attach. */
export interface FleetChatSession extends ChatSession {
  fleetKey?: string
  fleetName?: string
  issueNumber?: string | number
}

/** FleetPlanStatus extended with polling-error and failed-issue fields. */
export interface FleetPlanStatusExt extends FleetPlanStatus {
  last_poll_error?: string
  failed_issues?: Array<{
    issue_number: number
    session_id?: string
    failed_at?: string
    error?: string
  }>
  issues_retrying?: Array<{
    issue_number: number
    session_id?: string
    error?: string
    retry_count?: number
    last_failed_at?: string
  }>
}

/** FleetThread extended – backend may return agent_key alongside thread_key. */
export interface FleetThreadExt extends FleetThread {
  agent_key?: string
}

/** Structured plan data (fetchFleetPlan returns Record<string, unknown>). */
export interface FleetPlanData {
  name?: string
  description?: string
  setup_profile?: string
  created_from?: string
  channel?: {
    type?: string
    schedule?: string
    config?: Record<string, unknown>
    [k: string]: unknown
  }
  agents?: Record<string, FleetAgentDef>
  communication?: {
    flow?: CommFlowNode[]
  }
  artifacts?: Record<string, FleetArtifactDef>
  settings?: FleetSettings
  workspace_base_dir?: string
  project_context?: unknown
}

export interface CommFlowNode {
  role: string
  entry_point?: boolean
  talks_to?: string[]
}

export interface FleetAgentDef {
  name?: string
  mode?: string
  description?: string
  delegate?: { tool: string; [k: string]: unknown }
  behaviors?: string
  identity?: string
  capabilities?: Record<string, boolean>
  execution?: AgentExecutionConfig
  memory?: AgentMemoryConfig
  task_policy?: AgentTaskPolicy
  [k: string]: unknown
}

export interface FleetSettings {
  max_turns_per_agent?: number
  max_parallel_agents?: number
  max_wall_clock_minutes?: number
  routing_mode?: 'llm_mentions' | 'explicit_queue' | 'supervisor'
  task_board?: { claim_policy?: 'first_come' | 'capability_match' | 'supervisor_assigned' }
  memory_visibility?: 'scoped' | 'shared' | 'private_plus_handoffs'
  [k: string]: unknown
}

export interface AgentExecutionConfig {
  max_turns?: number
  timeout_minutes?: number
  parallelizable?: boolean
  workspace?: 'shared' | 'isolated' | 'none'
}

export interface AgentMemoryConfig {
  receives?: string[]
  private_work?: boolean
}

export interface AgentTaskPolicy {
  claims?: string[]
  max_concurrent?: number
}

export interface FleetArtifactDef {
  type?: string
  repo?: string
  path?: string
  auto_pr?: boolean
  [k: string]: unknown
}

export interface SidebarItem {
  key: string
  name: string
  subtitle?: string
  activated?: boolean
  badge?: string | null
  onDelete?: () => void
  onClone?: () => void
  onImportYaml?: () => void
  onExportYaml?: () => void
}

export interface SelectedItem {
  type: string
  key: string
}

/** Trace event – the events array inside FleetTrace is unknown[]. */
export interface TraceEvent {
  type?: string
  timestamp?: string
  session?: string
  text?: string
  tool_name?: string
  args?: unknown
  result?: unknown
  error?: string
  duration_ms?: number
  [k: string]: unknown
}

export interface AgentColor {
  bg: string
  border: string
  text: string
  label: string
}

// Agent identity colors for trace view
export const AGENT_COLORS: Record<string, AgentColor> = {
  po: { bg: 'rgba(59, 130, 246, 0.1)', border: 'rgba(59, 130, 246, 0.3)', text: '#60a5fa', label: 'PO' },
  architect: { bg: 'var(--brand-muted)', border: 'color-mix(in oklab, var(--brand) 30%, transparent)', text: 'var(--brand)', label: 'Architect' },
  ux: { bg: 'rgba(236, 72, 153, 0.1)', border: 'rgba(236, 72, 153, 0.3)', text: '#f472b6', label: 'UX' },
  dev: { bg: 'rgba(34, 197, 94, 0.1)', border: 'rgba(34, 197, 94, 0.3)', text: '#4ade80', label: 'Dev' },
  qa: { bg: 'rgba(234, 179, 8, 0.1)', border: 'rgba(234, 179, 8, 0.3)', text: '#facc15', label: 'QA' },
  e2e: { bg: 'rgba(20, 184, 166, 0.1)', border: 'rgba(20, 184, 166, 0.3)', text: '#2dd4bf', label: 'E2E' },
  system: { bg: 'rgba(107, 114, 128, 0.1)', border: 'rgba(107, 114, 128, 0.3)', text: '#9ca3af', label: 'System' },
}

/** Fallback palette for unknown agent keys (stable by hash). */
export const AGENT_COLOR_PALETTE: Omit<AgentColor, 'label'>[] = [
  { bg: 'rgba(6, 182, 212, 0.1)', border: 'rgba(6, 182, 212, 0.3)', text: '#22d3ee' },
  { bg: 'rgba(249, 115, 22, 0.1)', border: 'rgba(249, 115, 22, 0.3)', text: '#fb923c' },
  { bg: 'rgba(14, 165, 233, 0.1)', border: 'rgba(14, 165, 233, 0.3)', text: '#38bdf8' },
  { bg: 'rgba(244, 63, 94, 0.1)', border: 'rgba(244, 63, 94, 0.3)', text: '#fb7185' },
  { bg: 'rgba(132, 204, 22, 0.1)', border: 'rgba(132, 204, 22, 0.3)', text: '#a3e635' },
  { bg: 'rgba(99, 102, 241, 0.1)', border: 'rgba(99, 102, 241, 0.3)', text: '#818cf8' },
  { bg: 'rgba(217, 70, 239, 0.1)', border: 'rgba(217, 70, 239, 0.3)', text: '#e879f9' },
  { bg: 'rgba(245, 158, 11, 0.1)', border: 'rgba(245, 158, 11, 0.3)', text: '#fbbf24' },
]

function hashAgentKey(key: string): number {
  let h = 0
  for (let i = 0; i < key.length; i++) {
    h = ((h << 5) - h) + key.charCodeAt(i)
    h |= 0
  }
  return Math.abs(h)
}

export function getAgentColor(name: string): AgentColor {
  const known = AGENT_COLORS[name]
  if (known) return known
  const base = AGENT_COLOR_PALETTE[hashAgentKey(name) % AGENT_COLOR_PALETTE.length]
  return { ...base, label: name }
}

// Extract the agent role from a sub-agent session label.
// e.g., "fleet-juicytrade-po" -> "po", "fleet-myplan-architect" -> "architect"
export function extractAgentRole(sessionLabel: string): string | null {
  if (!sessionLabel) return null
  const parts = sessionLabel.split('-')
  const last = parts[parts.length - 1]
  // Check if the last segment is a known agent role
  if (AGENT_COLORS[last]) return last
  // Try last two segments for compound names (e.g., "qa-engineer" if ever used)
  if (parts.length >= 2) {
    const lastTwo = parts.slice(-2).join('-')
    if (AGENT_COLORS[lastTwo]) return lastTwo
  }
  return last
}

export function formatTimeAgo(dateStr: string): string {
  if (!dateStr) return ''
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  const diffHours = Math.floor(diffMins / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  return `${diffDays}d ago`
}

/** Slugify a display name into a valid fleet agent key. */
export function slugifyAgentKey(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9\s_-]/g, '')
    .replace(/[\s_]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
}

/** Minimal valid agent for new template / add-agent flows. */
export function createDefaultFleetAgent(name: string): FleetAgentDef {
  return {
    name,
    identity: `You are ${name}.`,
    behaviors: 'Follow the user instructions carefully and collaborate with other agents when needed.',
    tools: true,
    // Task board is always on; validation requires at least one agent with claims.
    capabilities: { general: true },
    task_policy: { claims: ['general'] },
  }
}

/** Insert an agent and keep the communication graph consistent. */
export function addAgentToFleetConfig(
  config: FleetPlanData,
  agentKey: string,
  agent: FleetAgentDef,
): FleetPlanData {
  const agents = { ...(config.agents || {}), [agentKey]: agent }
  const flow = [...(config.communication?.flow || [])]
  if (!flow.some(node => node.role === agentKey)) {
    const isFirst = Object.keys(config.agents || {}).length === 0
    flow.push({
      role: agentKey,
      talks_to: ['customer'],
      entry_point: isFirst || flow.every(node => !node.entry_point),
    })
  }
  return {
    ...config,
    agents,
    communication: { ...(config.communication || {}), flow },
  }
}

/** Remove an agent and scrub it from the communication graph. */
export function removeAgentFromFleetConfig(config: FleetPlanData, agentKey: string): FleetPlanData {
  const agents = { ...(config.agents || {}) }
  delete agents[agentKey]

  let flow = (config.communication?.flow || [])
    .filter(node => node.role !== agentKey)
    .map(node => ({
      ...node,
      talks_to: (node.talks_to || []).filter(target => target !== agentKey),
    }))

  if (flow.length > 0 && !flow.some(node => node.entry_point)) {
    flow = flow.map((node, i) => (i === 0 ? { ...node, entry_point: true } : node))
  }

  return {
    ...config,
    agents,
    communication: { ...(config.communication || {}), flow },
  }
}

/** Rename an agent key and rewrite communication + memory.receives references. */
export function renameAgentInFleetConfig(
  config: FleetPlanData,
  fromKey: string,
  toKey: string,
): FleetPlanData {
  if (!fromKey || !toKey || fromKey === toKey) return config
  const agents = { ...(config.agents || {}) }
  const existing = agents[fromKey]
  if (!existing) return config
  if (agents[toKey]) {
    throw new Error(`Role "@${toKey}" already exists`)
  }

  delete agents[fromKey]
  agents[toKey] = existing

  for (const [key, def] of Object.entries(agents)) {
    const receives = def.memory?.receives
    if (!receives?.length) continue
    const nextReceives = receives.map(r => (r === fromKey ? toKey : r))
    if (nextReceives.every((r, i) => r === receives[i])) continue
    agents[key] = {
      ...def,
      memory: { ...(def.memory || {}), receives: nextReceives },
    }
  }

  const flow = (config.communication?.flow || []).map(node => ({
    ...node,
    role: node.role === fromKey ? toKey : node.role,
    talks_to: (node.talks_to || []).map(target => (target === fromKey ? toKey : target)),
  }))

  return {
    ...config,
    agents,
    communication: { ...(config.communication || {}), flow },
  }
}

export function updateFleetSettings(config: FleetPlanData, settings: FleetSettings, setupProfileKey?: string): FleetPlanData {
  const next: FleetPlanData = { ...config, settings }
  if (setupProfileKey !== undefined) {
    next.setup_profile = setupProfileKey
  }
  return next
}
