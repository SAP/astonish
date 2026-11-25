import { teamFetch, getActiveTeam } from './teamContext'
import { botURL, BotAPIError, type BotScope } from './bots'
import type { FileAccessAdapter } from '@/components/chat/chatTypes'

export const BotFileKind = { File: 'file', Directory: 'directory' } as const
export const BotFilePreview = { Text: 'text', Download: 'download' } as const
export interface BotFileEntry { path: string; kind: 'file' | 'directory'; size_bytes: number; modified_at?: string; freshness: string; preview: 'text' | 'download' }
export interface BotFilePage { entries: BotFileEntry[]; next?: string }
export interface BotFileTarget { botId: string; scope: BotScope; path: string; runId?: string; reportSequence?: number; artifactId?: string }
export interface BotPublishedArtifact { id: string; home_id: string; relative_path: string; sha256: string; size_bytes: number; media_type: string; title: string; preview: 'text' | 'download' }
export interface BotFilePublication { schema_version: 1; bot_id: string; run_id: string; home_session_id: string; home_id: string; report_sequence: number; event_id: string; summary: string; artifacts: BotPublishedArtifact[] }
function hasInvalidPublicationPathCharacter(path: string): boolean {
  for (let i = 0; i < path.length; i++) {
    const code = path.charCodeAt(i)
    if (code === 0 || code === 10 || code === 13 || code === 92) return true
  }
  return false
}
export function isBotFilePublication(value: unknown): value is BotFilePublication {
  if (!value || typeof value !== 'object') return false
  const p = value as BotFilePublication
  return p.schema_version === 1 && !!p.bot_id && !!p.run_id && !!p.home_id && !!p.home_session_id && !!p.event_id && Number.isSafeInteger(p.report_sequence) && p.report_sequence > 0 && typeof p.summary === 'string' && Array.isArray(p.artifacts) && p.artifacts.length > 0 && new Set(p.artifacts.map(a => a?.id)).size === p.artifacts.length && p.artifacts.every(a => !!a && typeof a === 'object' && !!a.id && a.home_id === p.home_id && typeof a.relative_path === 'string' && !!a.relative_path && !hasInvalidPublicationPathCharacter(a.relative_path) && !a.relative_path.startsWith('/') && !a.relative_path.split('/').includes('..') && Number.isSafeInteger(a.size_bytes) && a.size_bytes >= 0 && /^[a-f0-9]{64}$/.test(a.sha256) && (a.preview === BotFilePreview.Text || a.preview === BotFilePreview.Download))
}
function fileURL(target: BotFileTarget, action = '') {
  const base = target.artifactId ? `/${encodeURIComponent(target.botId)}/runs/${encodeURIComponent(target.runId!)}/reports/${target.reportSequence}/artifacts/${encodeURIComponent(target.artifactId)}` : `/${encodeURIComponent(target.botId)}/files`
  const url = new URL(botURL(base + action, target.scope), window.location.origin)
  if (!target.artifactId) url.searchParams.set('path', target.path)
  return url.pathname + url.search
}
async function json<T>(url: string, signal?: AbortSignal, method = 'GET'): Promise<T> {
  const res = await teamFetch(url, { signal, method })
  if (!res.ok) throw new BotAPIError(res.status, await res.text())
  return res.json()
}
export function listBotFiles(target: BotFileTarget, signal?: AbortSignal, cursor?: string, showHidden = false): Promise<BotFilePage> {
  const url = new URL(fileURL(target), window.location.origin)
  if (showHidden) url.searchParams.set('show_hidden', 'true')
  if (cursor) url.searchParams.set('cursor', cursor)
  return json(url.pathname + url.search, signal)
}
export const statBotFile = (target: BotFileTarget, signal?: AbortSignal) => json<BotFileEntry>(fileURL(target, '/stat'), signal)
export const previewBotFile = (target: BotFileTarget, signal?: AbortSignal) => json<{ entry: BotFileEntry; content: string; truncated: boolean }>(fileURL(target, '/preview'), signal)
// Only the short authorization ticket is fetched. The browser streams the actual
// attachment using its HttpOnly cookie; no binary response is buffered in JS.
export async function downloadBotFile(target: BotFileTarget, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return
  const team = getActiveTeam()
  const ticket = await json<{ url: string }>(fileURL(target, '/download-ticket'), signal, 'POST')
  if (signal?.aborted || team !== getActiveTeam()) return
  const url = new URL(ticket.url, window.location.origin)
  const expected = new URL(fileURL(target, '/download'), window.location.origin)
  if (url.origin !== expected.origin || url.pathname !== expected.pathname || url.searchParams.get('scope') !== expected.searchParams.get('scope') || url.searchParams.get('path') !== expected.searchParams.get('path')) throw new Error('Invalid download destination')
  const link = document.createElement('a')
  link.href = url.href; link.download = target.path.split('/').pop() || 'file'; link.rel = 'noopener'
  document.body.appendChild(link); link.click(); link.remove()
}
export function botFileAccess(target: BotFileTarget, preview: BotFileEntry['preview'], freshness: string): FileAccessAdapter {
  return { identity: `${getActiveTeam()}/${target.scope}/${target.botId}/${target.runId || ''}/${target.reportSequence || ''}/${target.artifactId || target.path}`, freshness, preview: preview === BotFilePreview.Text, readPreview: signal => previewBotFile(target, signal), download: signal => downloadBotFile(target, signal) }
}
export function invalidatesBotFiles(error: unknown) { return error instanceof BotAPIError && [401, 403, 404, 410].includes(error.status) }
