import { useEffect, useState } from 'react'
import { ChevronRight, Clock, Plus, Trash2 } from 'lucide-react'
import BotDeleteDialog from './BotDeleteDialog'
import { Button } from '@/components/ui/button'
import { BotStatus, listBots, type Bot, type BotScope } from '@/api/bots'
import { getMe } from '@/api/auth'
import { getActiveTeam } from '@/api/teamContext'
import { currentIntakeNamespace, readIntakePointer, type IntakePointer } from '@/api/botIntake'
import NewBotDialog from './NewBotDialog'
import { botPath } from './botRoute'
import { useWorkspaceResource } from './workspaceResource'
import { cn } from '@/lib/utils'
import { formatTimeAgo, sidebarHeaderClass, sidebarHeadingClass, sidebarActionClass, sidebarRowClass, sidebarRowPaddingClass, sidebarTitleClass, sidebarMetadataClass, sidebarSeparatorClass, sidebarTrashClass } from '../chat/sidebarPresentation'

interface BotSidebarProps {
  activeId?: string
  activeScope?: BotScope
  collapsed?: boolean
  onExpand?: () => void
  onToggleSidebar?: () => void
}

export default function BotSidebar(props: BotSidebarProps) {
  // Keep the legacy collapsed prop compatible without mounting hidden data or controls.
  return props.collapsed ? null : <ExpandedBotSidebar {...props} />
}

const PERSONAL_SCOPE = 'personal' as const

function ExpandedBotSidebar({ activeId, activeScope, onToggleSidebar }: BotSidebarProps) {
  const scope = PERSONAL_SCOPE
  const [deleteTarget, setDeleteTarget] = useState<Bot | null>(null)
  const [simpleCreate, setSimpleCreate] = useState(false)
  const [revision, setRevision] = useState(0)
  const [pointer, setPointer] = useState<IntakePointer | null>(null)
  const openIntake = (resumeId?: string) => window.dispatchEvent(new CustomEvent('astonish:source-intake', { detail: { scope, resumeId } }))
  const team = getActiveTeam()
  const [viewer, setViewer] = useState<string>()
  useEffect(() => { setDeleteTarget(null) }, [team, viewer, scope])
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
  return <section aria-label="Bots" className="border-b border-border max-h-[40vh] overflow-y-auto pb-3">
    <div className={sidebarHeaderClass}>
      <h2 className={sidebarHeadingClass}>Bots</h2>
      <div className="flex items-center gap-0.5">
        <Button type="button" size="icon" variant="ghost" className={sidebarActionClass} aria-label="New Bot" title="New Bot" onClick={() => setSimpleCreate(true)}><Plus className="size-4" /></Button>
        {onToggleSidebar && <Button type="button" size="icon" variant="ghost" className={sidebarActionClass} aria-label="Hide sidebar" title="Hide sidebar" onClick={onToggleSidebar}><ChevronRight className="size-4 rotate-180" /></Button>}
      </div>
    </div>
    <div className="space-y-0.5 px-2">
      {resource.loading && <p role="status" className="px-3 text-xs text-muted-foreground">Loading Bots…</p>}
      {resource.error && <div role="alert" className="px-3 text-xs text-destructive">{resource.error}<Button size="sm" variant="ghost" aria-label="Retry Bots" title={resource.error} onClick={resource.retry}>Retry Bots</Button></div>}
      {!resource.loading && !resource.error && bots.length === 0 && <p className="px-3 text-xs text-muted-foreground">No Bots yet. Create a Bot.</p>}
      {bots.map(bot => <BotSidebarItem key={`${viewer}/${team}/${bot.scope}/${bot.id}`} bot={bot} activeId={activeId} activeScope={activeScope} onDelete={viewer === bot.owner_id ? () => setDeleteTarget(bot) : undefined} />)}
    </div>
    {deleteTarget && <BotDeleteDialog key={`${viewer}/${team}/${deleteTarget.scope}/${deleteTarget.id}`} bot={deleteTarget} onClose={() => setDeleteTarget(null)} />}
    {pointer && <Button className="mx-3.5 mt-2" size="sm" variant="outline" onClick={() => openIntake(pointer.id)}>Resume intake</Button>}
    {simpleCreate && <NewBotDialog onClose={() => setSimpleCreate(false)} onSources={() => { setSimpleCreate(false); openIntake() }} />}
  </section>
}

function BotSidebarItem({ bot, activeId, activeScope, onDelete }: { bot: Bot; activeId?: string; activeScope?: BotScope; onDelete?: () => void }) {
  const selected = bot.id === activeId && bot.scope === (activeScope || 'personal')
  const date = formatTimeAgo(bot.home_updated_at || bot.updated_at)
  return <div className={cn(sidebarRowClass, 'relative', selected ? 'bg-[color:var(--item-active)]' : 'hover:bg-[color:var(--item-hover)]')}>
    <a href={`#${botPath(bot.id, bot.scope)}`} aria-label={bot.name} title={bot.name} aria-current={selected ? 'page' : undefined} className={cn('block min-w-0 rounded-[var(--radius-md)] focus-visible:outline focus-visible:outline-ring', sidebarRowPaddingClass)}>
      <div className="min-w-0 pr-6">
        <p className={sidebarTitleClass}>{bot.name}</p>
        <div className={sidebarMetadataClass}>
          <Clock size={10} aria-hidden="true" />
          <span>{date}</span>
          {bot.message_count !== undefined && <>
            {date && <span className={sidebarSeparatorClass} />}
            <span>{bot.message_count} msg{bot.message_count !== 1 ? 's' : ''}</span>
          </>}
        </div>
      </div>
    </a>
    {onDelete && <button type="button" onClick={onDelete} aria-label={`Delete ${bot.name}`} title={`Delete ${bot.name}`} className={cn(sidebarTrashClass, 'absolute right-3 top-2.5 shrink-0')}><Trash2 size={12} aria-hidden="true" /></button>}
  </div>
}
