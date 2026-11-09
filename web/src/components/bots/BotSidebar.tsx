import { useEffect, useState } from 'react'
import { Bot as BotIcon, Trash2 } from 'lucide-react'
import BotDeleteDialog from './BotDeleteDialog'
import { Button } from '@/components/ui/button'
import { BotStatus, listBots, type Bot, type BotScope } from '@/api/bots'
import { getMe } from '@/api/auth'
import { getActiveTeam } from '@/api/teamContext'
import { currentIntakeNamespace, readIntakePointer, type IntakePointer } from '@/api/botIntake'
import NewBotDialog from './NewBotDialog'
import { botPath } from './botRoute'
import { useWorkspaceResource } from './workspaceResource'

interface BotSidebarProps {
  activeId?: string
  activeScope?: BotScope
  collapsed?: boolean
  onExpand?: () => void
}

export default function BotSidebar({ activeId, activeScope, collapsed = false, onExpand }: BotSidebarProps) {
  const [scope, setScope] = useState<BotScope>(activeScope || 'personal')
  const [deleteTarget, setDeleteTarget] = useState<Bot | null>(null)
  const [simpleCreate, setSimpleCreate] = useState(false)
  const [revision, setRevision] = useState(0)
  const [pointer, setPointer] = useState<IntakePointer | null>(null)
  const openIntake = (resumeId?: string) => window.dispatchEvent(new CustomEvent('astonish:source-intake', { detail: { scope, resumeId } }))
  const team = getActiveTeam()
  const [viewer, setViewer] = useState<string>()
  useEffect(() => { setDeleteTarget(null) }, [team, viewer, scope])
  // A deep link selects its exact scope; manual filters remain usable until navigation changes.
  useEffect(() => { if (activeScope) setScope(activeScope) }, [activeId, activeScope])
  useEffect(() => { let cancelled = false; setViewer(undefined); void getMe().then(me => { if (!cancelled) setViewer(me.user.id) }).catch(() => {}); return () => { cancelled = true } }, [team])
  useEffect(() => {
    let cancelled = false
    setPointer(null)
    const refresh = () => { void currentIntakeNamespace(scope, team).then(namespace => {
      if (!cancelled) { const next = readIntakePointer(namespace); setPointer(next?.scope === scope ? next : null) }
    }).catch(() => { if (!cancelled) setPointer(null) }) }
    refresh()
    window.addEventListener('astonish:intake-updated', refresh)
    window.addEventListener('storage', refresh)
    return () => { cancelled = true; window.removeEventListener('astonish:intake-updated', refresh); window.removeEventListener('storage', refresh) }
  }, [scope, team])
  useEffect(() => {
    const refresh = () => setRevision(v => v + 1)
    window.addEventListener('astonish:bots-updated', refresh)
    const timer = setInterval(refresh, 15000)
    return () => { clearInterval(timer); window.removeEventListener('astonish:bots-updated', refresh) }
  }, [])
  const resource = useWorkspaceResource(signal => listBots(scope, signal), `${viewer || ''}/${team || ''}/${scope}`, revision)
  // Navigation only: soft-deleted Bots retain their history and direct URLs.
  const bots = (resource.data?.bots || []).filter(bot => bot.status !== BotStatus.Deleted)
  return <section aria-label="Bots" className={collapsed ? 'w-full space-y-2 border-b border-border pb-2 max-h-[40vh] overflow-y-auto' : 'border-b border-border p-3 space-y-2 max-h-[40vh] overflow-y-auto'}>
    {collapsed ? <Button size="icon" variant="ghost" onClick={onExpand} aria-label="Show Bots" title={`Show ${scope === 'personal' ? 'Private' : 'Team'} Bots`} className="mx-auto flex"><BotIcon /></Button> : <>
      <div className="flex items-center justify-between"><h2 className="font-semibold text-sm">Bots</h2><Button size="sm" variant="ghost" onClick={() => setSimpleCreate(true)}>New Bot</Button></div>
      <div className="flex gap-1">{(['personal', 'team'] as const).map(s => <Button size="sm" aria-pressed={scope === s} variant={scope === s ? 'secondary' : 'ghost'} key={s} onClick={() => setScope(s)}>{s === 'personal' ? 'Private' : 'Team'}</Button>)}</div>
    </>}
    {resource.loading && <p role="status" className="text-xs">{collapsed ? '…' : 'Loading Bots…'}<span className={collapsed ? 'sr-only' : 'hidden'}>Loading Bots</span></p>}
    {resource.error && <div role="alert" className="text-xs text-destructive"><span className={collapsed ? 'sr-only' : ''}>{resource.error}</span><Button size="sm" variant="ghost" aria-label="Retry Bots" title={resource.error} onClick={resource.retry}>{collapsed ? '!' : 'Retry Bots'}</Button></div>}
    {!resource.loading && !resource.error && bots.length === 0 && <p className={collapsed ? 'sr-only' : 'text-xs text-muted-foreground'}>No Bots yet. Create a reviewed draft.</p>}
    {bots.map(bot => <BotSidebarItem key={`${viewer}/${team}/${bot.scope}/${bot.id}`} bot={bot} activeId={activeId} activeScope={activeScope} collapsed={collapsed} onDelete={viewer === bot.owner_id ? () => setDeleteTarget(bot) : undefined} />)}
    {deleteTarget && <BotDeleteDialog key={`${viewer}/${team}/${deleteTarget.scope}/${deleteTarget.id}`} bot={deleteTarget} onClose={() => setDeleteTarget(null)} />}
    {!collapsed && pointer && <Button size="sm" variant="outline" onClick={() => openIntake(pointer.id)}>Resume intake</Button>}
    {simpleCreate && <NewBotDialog onClose={() => setSimpleCreate(false)} onSources={() => { setSimpleCreate(false); openIntake() }} />}
  </section>
}

function BotSidebarItem({ bot, activeId, activeScope, collapsed, onDelete }: { bot: Bot; activeId?: string; activeScope?: BotScope; collapsed: boolean; onDelete?: () => void }) {
  const selected = bot.id === activeId && bot.scope === (activeScope || 'personal')
  return <div className={`group flex items-center rounded hover:bg-accent ${collapsed ? 'flex-col' : ''} ${selected ? 'bg-accent' : ''}`}>
    <a href={`#${botPath(bot.id, bot.scope)}`} aria-label={collapsed ? bot.name : undefined} title={bot.name} aria-current={selected ? 'page' : undefined} className="block min-w-0 flex-1 rounded p-2 text-sm focus-visible:outline focus-visible:outline-ring">
      {collapsed ? <BotIcon className="mx-auto size-4" aria-hidden="true" /> : <span className="block truncate font-medium">{bot.name}</span>}
    </a>
    {onDelete && <button type="button" onClick={onDelete} aria-label={`Delete ${bot.name}`} title={`Delete ${bot.name}`} className="shrink-0 cursor-pointer rounded p-2 text-destructive opacity-100 transition-opacity md:opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 focus-visible:outline focus-visible:outline-ring hover:bg-destructive/20"><Trash2 size={14} aria-hidden="true" /></button>}
  </div>
}
