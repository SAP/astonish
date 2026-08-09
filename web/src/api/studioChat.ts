/**
 * API client for Studio Chat
 */

import { teamFetch } from './teamContext'

const API_BASE = '/api/studio'

// --- Types ---

export interface ChatSession {
  id: string
  title: string
  createdAt: string
  updatedAt: string
  messageCount: number
}

export interface SessionHistory {
  id: string
  title: string
  messages: ChatMessage[]
}

export interface ChatMessage {
  role: string
  content: string
  tool_calls?: ToolCall[]
  tool_results?: ToolResult[]
  [key: string]: unknown
}

export interface ToolCall {
  id: string
  name: string
  args: Record<string, unknown>
}

export interface ToolResult {
  tool_call_id: string
  content: string
}

export type SSEEventCallback = (eventType: string, data: Record<string, unknown>) => void
export type ErrorCallback = (error: Error) => void
export type DoneCallback = () => void

export interface AttachmentPayload {
  filename: string
  mimeType: string
  data: string  // base64-encoded content (no data-url prefix)
}


export interface SessionModelStatus {
  pinnedProvider: string
  pinnedModel: string
  effectiveProvider: string
  effectiveModel: string
  credentialsAvailable: boolean
  availableProviders: string[]
}

export interface ConnectChatParams {
  sessionId?: string
  message?: string
  attachments?: AttachmentPayload[]
  systemContext?: string
  planMode?: boolean
  pinnedToolGroups?: string[]
  autoApprove?: boolean
  provider?: string
  model?: string
  memoryScope?: 'personal' | 'team'
  onEvent: SSEEventCallback
  onError?: ErrorCallback
  onDone?: DoneCallback
}

// --- API Functions ---

export async function fetchSessions(): Promise<ChatSession[]> {
  const response = await teamFetch(`${API_BASE}/sessions`)
  if (!response.ok) {
    throw new Error(`Failed to fetch sessions: ${response.statusText}`)
  }
  return response.json()
}

export async function fetchSessionHistory(id: string): Promise<SessionHistory> {
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(id)}`)
  if (!response.ok) {
    throw new Error(`Failed to fetch session: ${response.statusText}`)
  }
  return response.json()
}

export async function deleteSession(id: string): Promise<void> {
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!response.ok) {
    throw new Error(`Failed to delete session: ${response.statusText}`)
  }
}

export interface SubtaskEventItem {
  type: string
  tool_name?: string
  tool_args?: unknown
  tool_result?: unknown
  text?: string
}

export async function fetchSubtaskEvents(sessionId: string, taskName: string): Promise<SubtaskEventItem[]> {
  const params = new URLSearchParams({ task_name: taskName })
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/subtask-events?${params}`)
  if (!response.ok) {
    return []
  }
  const data = await response.json()
  return data.events || []
}

export function connectChat({ sessionId, message, attachments, systemContext, planMode, pinnedToolGroups, autoApprove, provider, model, memoryScope, onEvent, onError, onDone }: ConnectChatParams): AbortController {
  const controller = new AbortController()

  const run = async () => {
    try {
      const body: Record<string, unknown> = {
        sessionId: sessionId || '',
        message: message || '',
        autoApprove: !!autoApprove,
        ...(provider ? { provider } : {}),
        ...(model ? { model } : {}),
        ...(memoryScope ? { memoryScope } : {}),
      }
      if (attachments && attachments.length > 0) {
        body.attachments = attachments
      }
      if (systemContext) {
        body.systemContext = systemContext
      }
      if (planMode) {
        body.planMode = true
      }
      if (pinnedToolGroups && pinnedToolGroups.length > 0) {
        body.pinnedToolGroups = pinnedToolGroups
      }
      const response = await teamFetch(`${API_BASE}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: controller.signal,
        body: JSON.stringify(body),
      })

      if (!response.ok) {
        const text = await response.text()
        throw new Error(text || `HTTP ${response.status}`)
      }

      const reader = response.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { value, done } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const blocks = buffer.split('\n\n')
        buffer = blocks.pop()!

        for (const block of blocks) {
          if (!block.trim()) continue
          const lines = block.split('\n')
          let eventType = 'message'
          let dataStr = ''

          for (const line of lines) {
            if (line.startsWith('event: ')) {
              eventType = line.slice(7).trim()
            } else if (line.startsWith('data: ')) {
              dataStr = line.slice(6)
            }
          }

          if (dataStr) {
            try {
              const data = JSON.parse(dataStr)
              onEvent(eventType, data)
            } catch (e) {
              console.error('Failed to parse SSE data:', e, dataStr)
            }
          }
        }
      }

      if (onDone) onDone()
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') {
        if (onDone) onDone()
      } else {
        if (onError) onError(err instanceof Error ? err : new Error(String(err)))
      }
    }
  }

  run()
  return controller
}

export async function stopChat(sessionId: string): Promise<void> {
  try {
    await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/stop`, {
      method: 'POST',
    })
  } catch (err) {
    console.warn('Failed to stop chat:', err)
  }
}

/**
 * Check if a session has an active background runner.
 */
export async function fetchSessionStatus(sessionId: string): Promise<{ sessionId: string; running: boolean; eventCount?: number }> {
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/status`)
  if (!response.ok) {
    throw new Error(`Failed to fetch session status: ${response.statusText}`)
  }
  return response.json()
}

/**
 * Reconnect to an active background chat runner via SSE.
 * Returns an AbortController that can be used to disconnect (without stopping the runner).
 */
export function connectChatStream({ sessionId, onEvent, onError, onDone }: {
  sessionId: string
  onEvent: SSEEventCallback
  onError?: ErrorCallback
  onDone?: DoneCallback
}): AbortController {
  const controller = new AbortController()

  const run = async () => {
    try {
      const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/stream`, {
        method: 'GET',
        signal: controller.signal,
      })

      if (!response.ok) {
        const text = await response.text()
        throw new Error(text || `HTTP ${response.status}`)
      }

      const reader = response.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { value, done } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const blocks = buffer.split('\n\n')
        buffer = blocks.pop()!

        for (const block of blocks) {
          if (!block.trim()) continue
          const lines = block.split('\n')
          let eventType = 'message'
          let dataStr = ''

          for (const line of lines) {
            if (line.startsWith('event: ')) {
              eventType = line.slice(7).trim()
            } else if (line.startsWith('data: ')) {
              dataStr = line.slice(6)
            }
          }

          if (dataStr) {
            try {
              const data = JSON.parse(dataStr)
              onEvent(eventType, data)
            } catch (e) {
              console.error('Failed to parse SSE data:', e, dataStr)
            }
          }
        }
      }

      if (onDone) onDone()
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') {
        if (onDone) onDone()
      } else {
        if (onError) onError(err instanceof Error ? err : new Error(String(err)))
      }
    }
  }

  run()
  return controller
}

// --- Artifact APIs ---

/** Fetch the raw text content of a file artifact for the in-browser viewer. */
export async function fetchArtifactContent(path: string, sessionId?: string): Promise<string> {
  let url = `${API_BASE}/artifacts/content?path=${encodeURIComponent(path)}`
  if (sessionId) url += `&session=${encodeURIComponent(sessionId)}`
  const resp = await teamFetch(url)
  if (!resp.ok) throw new Error(`Failed to fetch artifact content: ${resp.status}`)
  return resp.text()
}

/**
 * Fetch a binary artifact (e.g. MP4) via the download endpoint.
 * Uses teamFetch so X-Astonish-Team / CSRF headers are applied — a bare
 * `<video src={downloadUrl}>` cannot send those headers.
 */
export async function fetchArtifactBlob(path: string, sessionId?: string): Promise<Blob> {
  let url = `${API_BASE}/artifacts?path=${encodeURIComponent(path)}`
  if (sessionId) url += `&session=${encodeURIComponent(sessionId)}`
  const resp = await teamFetch(url)
  if (!resp.ok) throw new Error(`Failed to fetch artifact: ${resp.status}`)
  return resp.blob()
}

/** Get the download URL for a raw file artifact. */
export function getArtifactDownloadUrl(path: string, sessionId?: string): string {
  let url = `${API_BASE}/artifacts?path=${encodeURIComponent(path)}`
  if (sessionId) url += `&session=${encodeURIComponent(sessionId)}`
  return url
}

/** Get the download URL for a markdown artifact converted to PDF by the backend. */
export function getArtifactPDFUrl(path: string, sessionId?: string): string {
  let url = `${API_BASE}/artifacts/pdf?path=${encodeURIComponent(path)}`
  if (sessionId) url += `&session=${encodeURIComponent(sessionId)}`
  return url
}

// --- Network Denial APIs ---

export interface NetworkDenial {
  chunk_id?: string
  host: string
  port: number
  binary?: string
  rationale?: string
  security_notes?: string
  broader_pattern?: string
}

export interface NetworkDenialsResponse {
  denials: NetworkDenial[]
  sandbox_name: string
  error?: string
}

/** Fetch pending network denials for a session's sandbox. */
export async function fetchNetworkDenials(sessionId: string): Promise<NetworkDenialsResponse> {
  const resp = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/network-denials`)
  if (!resp.ok) throw new Error(`Failed to fetch network denials: ${resp.status}`)
  return resp.json()
}

/** Approve a specific network denial (exact host:port). */
export async function approveNetworkGrant(sessionId: string, chunkId: string, sandboxName: string): Promise<void> {
  const resp = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/network-grants/approve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ chunk_id: chunkId, sandbox_name: sandboxName }),
  })
  if (!resp.ok) throw new Error(`Failed to approve network grant: ${resp.status}`)
}

/** Approve a broader pattern (e.g., **.cloud.sap:443). */
export async function approveNetworkGrantBroader(sessionId: string, host: string, port: number, sandboxName: string): Promise<void> {
  const resp = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/network-grants/approve-broader`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ host, port, sandbox_name: sandboxName }),
  })
  if (!resp.ok) throw new Error(`Failed to approve broader network grant: ${resp.status}`)
}

/** Deny a network access request. */
export async function denyNetworkGrant(sessionId: string, chunkId: string, sandboxName: string, reason?: string): Promise<void> {
  const resp = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/network-grants/deny`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ chunk_id: chunkId, sandbox_name: sandboxName, reason: reason || '' }),
  })
  if (!resp.ok) throw new Error(`Failed to deny network grant: ${resp.status}`)
}

/** Fetch the model status for a session. */
export async function fetchSessionModelStatus(sessionId: string): Promise<SessionModelStatus> {
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/model-status`)
  if (!response.ok) {
    throw new Error(`Failed to fetch session model status: ${response.statusText}`)
  }
  return response.json()
}

/** Update the pinned model for a session. */
export async function patchSessionModel(sessionId: string, provider: string, model: string): Promise<SessionModelStatus> {
  const response = await teamFetch(`${API_BASE}/sessions/${encodeURIComponent(sessionId)}/model`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider, model }),
  })
  if (!response.ok) {
    throw new Error(`Failed to patch session model: ${response.statusText}`)
  }
  return response.json()
}

/** Fetch available provider names (session-less, uses effective config). */
export async function fetchAvailableProviders(): Promise<string[]> {
  const response = await teamFetch('/api/settings/providers/effective')
  if (!response.ok) return []
  const data = await response.json()
  return Object.keys(data.providers || {}).sort()
}
