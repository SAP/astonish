import { useEffect, useMemo, useState } from 'react'
import { Button } from '@/components/ui/button'
import EmbeddedFileViewer from '@/components/chat/EmbeddedFileViewer'
import ArtifactCard from '@/components/chat/ArtifactCard'
import { fileTypeFromFileName } from '@/utils/artifactMedia'
import { getActiveTeam } from '@/api/teamContext'
import type { Bot, BotScope } from '@/api/bots'
import { BotFileKind, botFileAccess, invalidatesBotFiles, listBotFiles, type BotFileEntry, type BotFilePage, type BotFilePublication, type BotFileTarget } from '@/api/botFiles'

export function BotFileSelection({ target, entry }: { target: BotFileTarget; entry: BotFileEntry }) {
  const access = useMemo(() => botFileAccess(target, entry.preview, entry.freshness), [target.botId, target.scope, target.path, target.runId, target.reportSequence, target.artifactId, entry.preview, entry.freshness])
  const name = target.path.split('/').pop() || target.path
  return <EmbeddedFileViewer key={`${access.identity}/${access.freshness}`} artifact={{ path: target.path, fileName: name, fileType: fileTypeFromFileName(name), toolName: 'write_file' }} fileAccess={access} />
}
function PublishedFile({ publication: p, artifact: a, scope }: { publication: BotFilePublication; artifact: BotFilePublication['artifacts'][number]; scope: BotScope }) {
  const [open, setOpen] = useState(false)
  const target = useMemo(() => ({ botId: p.bot_id, scope, path: a.relative_path, runId: p.run_id, reportSequence: p.report_sequence, artifactId: a.id }), [p.bot_id, scope, a.relative_path, p.run_id, p.report_sequence, a.id])
  const access = useMemo(() => botFileAccess(target, a.preview, a.sha256), [target, a.preview, a.sha256])
  return <div data-artifact-id={a.id} onKeyDown={e => { if (e.key === 'Escape' && open) { e.stopPropagation(); setOpen(false) } }}>
    <ArtifactCard data={{ type: 'artifact', path: a.relative_path, toolName: 'write_file' }} fileAccess={access} sizeBytes={a.size_bytes} onOpenInPanel={a.preview === 'text' ? () => setOpen(v => !v) : undefined} />
    {open && <><Button onClick={() => setOpen(false)}>Close preview</Button><BotFileSelection target={target} entry={{ path: a.relative_path, kind: BotFileKind.File, size_bytes: a.size_bytes, preview: a.preview, freshness: a.sha256 }} /></>}
  </div>
}
export function BotPublicationDelivery({ publication, scope }: { publication: BotFilePublication; scope: BotScope }) {
  return <section aria-label="Published files" data-publication-id={publication.event_id} className="my-3 space-y-2"><p className="whitespace-pre-wrap">{publication.summary}</p>{publication.artifacts.map(a => <PublishedFile key={a.id} publication={publication} artifact={a} scope={scope} />)}</section>
}
export default function BotFilesPanel({ bot, enabled, revision = 0 }: { bot: Pick<Bot, 'id' | 'scope' | 'home' | 'status' | 'permanent_delete'>; enabled: boolean; revision?: number }) {
  return <Files key={`${getActiveTeam()}/${bot.scope}/${bot.id}/${bot.home?.id || ''}`} bot={bot} enabled={enabled} revision={revision} />
}
function Files({ bot, enabled, revision }: { bot: Pick<Bot, 'id' | 'scope' | 'home' | 'status' | 'permanent_delete'>; enabled: boolean; revision: number }) {
  const [path, setPath] = useState(''), [attempt, setAttempt] = useState(0), [cursor, setCursor] = useState('')
  const [showHidden, setShowHidden] = useState(false)
  const [page, setPage] = useState<BotFilePage>(), [selection, setSelection] = useState<BotFileEntry>()
  const [error, setError] = useState(''), [loading, setLoading] = useState(false)
  const available = enabled && !!bot.home && !bot.permanent_delete && bot.status !== 'deleted'
  useEffect(() => {
    if (!available) { setSelection(undefined); setPage(undefined); return }
    const controller = new AbortController()
    setLoading(true); setError(''); setSelection(undefined)
    listBotFiles({ botId: bot.id, scope: bot.scope, path }, controller.signal, cursor, showHidden).then(value => {
      if (!controller.signal.aborted) setPage(old => cursor ? { ...value, entries: [...(old?.entries || []), ...value.entries] } : value)
    }).catch(e => { if (!controller.signal.aborted) { setError(e instanceof Error ? e.message : String(e)); if (invalidatesBotFiles(e)) setPage(undefined) } }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [available, bot.id, bot.scope, path, attempt, revision, cursor, showHidden])
  useEffect(() => {
    const refresh = (event: Event) => {
      if (event instanceof CustomEvent && event.detail?.botId !== bot.id) return
      if (available) { setCursor(''); setAttempt(v => v + 1) }
    }
    window.addEventListener('online', refresh); window.addEventListener('astonish:bot-files-refresh', refresh)
    return () => { window.removeEventListener('online', refresh); window.removeEventListener('astonish:bot-files-refresh', refresh) }
  }, [available, bot.id])
  const navigate = (next: string) => { setPage(undefined); setSelection(undefined); setCursor(''); setPath(next) }
  if (!enabled) return null
  return <section aria-label="Bot files" className="space-y-3" onKeyDown={e => { if (e.key === 'Escape' && selection) { e.preventDefault(); e.stopPropagation(); setSelection(undefined) } }}>
    <h2 className="font-semibold">Files</h2><p className="text-sm text-muted-foreground">Actual retained Home files. Instructions, MEMORY.md, knowledge, tasks and skills live here. Published files in Chat are separate immutable snapshots. Protected system files are excluded.</p>
    {!bot.home ? <p role="status">Home is not prepared. Browsing does not create a Home or run the Bot.</p> : !available ? <p role="status">Bot files are unavailable.</p> : <>
      <nav aria-label="File breadcrumbs" className="flex flex-wrap gap-2"><Button variant="link" onClick={() => navigate('')}>Home</Button>{path.split('/').filter(Boolean).map((part, i, parts) => <Button key={i} variant="link" onClick={() => navigate(parts.slice(0, i + 1).join('/'))}>{part}</Button>)}</nav>
      <div className="flex flex-wrap items-center gap-3"><Button disabled={loading} onClick={() => { setCursor(''); setAttempt(v => v + 1) }}>Refresh files</Button>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={showHidden} onChange={e => { setShowHidden(e.target.checked); navigate(''); setAttempt(v => v + 1) }} />Show hidden files</label>
      </div>
      {loading && <p role="status">Loading directory…</p>}
      {error && <p role="alert">{page ? 'Last known listing · ' : ''}{error}<Button onClick={() => setAttempt(v => v + 1)}>Retry files</Button></p>}
      {!loading && !error && !page?.entries.length && <p>Directory is empty.</p>}
      <ul>{page?.entries.map(entry => <li key={entry.path} className="flex flex-wrap items-center gap-2"><Button className="min-w-0 max-w-full h-auto whitespace-normal break-all text-left" variant="link" disabled={!!error || loading} onClick={() => entry.kind === BotFileKind.Directory ? navigate(entry.path) : setSelection(entry)}>{entry.kind === BotFileKind.Directory ? 'Folder: ' : ''}{entry.path.split('/').pop()}</Button><span className="text-xs text-muted-foreground">{entry.kind === BotFileKind.File ? `${entry.size_bytes.toLocaleString()} bytes` : ''}</span></li>)}</ul>
      {page?.next && <Button disabled={loading || !!error} onClick={() => setCursor(page.next!)}>Load more files</Button>}
      {selection && <section aria-label="File preview"><Button onClick={() => setSelection(undefined)}>Close preview</Button><BotFileSelection target={{ botId: bot.id, scope: bot.scope, path: selection.path }} entry={selection} /></section>}
    </>}
  </section>
}
