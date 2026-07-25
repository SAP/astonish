import { ChevronRight, Clock, Loader2, MessageSquare, Plus, Search, Trash2, Users } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import type { ChatSession } from '../../api/studioChat'

// Extended ChatSession with optional fleet fields
interface SidebarSession extends ChatSession {
  fleetKey?: string
  fleetName?: string
}

interface SessionSidebarProps {
  sessions: SidebarSession[]
  activeSessionId: string | null
  sessionFilter: string
  onSessionFilterChange: (filter: string) => void
  isLoadingSessions: boolean
  sidebarCollapsed: boolean
  onToggleSidebar: () => void
  onSelectSession: (session: SidebarSession) => void
  onNewSession: () => void
  onDeleteSession: (e: React.MouseEvent, id: string) => void
  onStartFleet: () => void
  theme: string
}

function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const mins = Math.floor(diffMs / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}d ago`
  return date.toLocaleDateString()
}

export default function SessionSidebar({
  sessions,
  activeSessionId,
  sessionFilter,
  onSessionFilterChange,
  isLoadingSessions,
  sidebarCollapsed,
  onToggleSidebar,
  onSelectSession,
  onNewSession,
  onDeleteSession,
  onStartFleet,
}: SessionSidebarProps) {
  if (sidebarCollapsed) {
    return (
      <div className="flex w-12 flex-col items-center gap-2 border-r border-border bg-[color:var(--sidebar-background)] py-3">
        <Button type="button" variant="ghost" size="icon" onClick={onToggleSidebar} title="Show sidebar" aria-label="Show sidebar">
          <ChevronRight />
        </Button>
        <Button type="button" variant="ghost" size="icon" onClick={onNewSession} title="New conversation" aria-label="New conversation">
          <Plus />
        </Button>
      </div>
    )
  }

  return (
    <div className="flex w-[260px] min-w-[260px] flex-shrink-0 flex-col border-r border-border bg-[color:var(--sidebar-background)]">
      <div className="flex items-center justify-between px-[18px] pt-4 pb-3">
        <span className="text-[13px] font-semibold text-foreground">Conversations</span>
        <div className="flex items-center gap-0.5">
          <Button type="button" variant="ghost" size="icon" onClick={onStartFleet} title="Start fleet session" aria-label="Start fleet session" className="size-7 text-muted-foreground">
            <Users className="size-4" />
          </Button>
          <Button type="button" variant="ghost" size="icon" onClick={onNewSession} title="New conversation" aria-label="New conversation" className="size-7 text-muted-foreground">
            <Plus className="size-4" />
          </Button>
          <Button type="button" variant="ghost" size="icon" onClick={onToggleSidebar} title="Hide sidebar" aria-label="Hide sidebar" className="size-7 text-muted-foreground">
            <ChevronRight className="size-4 rotate-180" />
          </Button>
        </div>
      </div>

      <div className="px-3.5 pb-3">
        <div className="relative">
          <Search className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-[color:var(--text-faint)]" />
          <Input
            type="text"
            value={sessionFilter}
            onChange={(e) => onSessionFilterChange(e.target.value)}
            placeholder="Search conversations..."
            className="h-9 border-[color:var(--border-soft)] bg-[color:var(--search-bg)] pl-8 text-[12.5px] shadow-none"
          />
        </div>
      </div>

      <div className="flex-1 space-y-0.5 overflow-y-auto px-2 pb-3">
        {isLoadingSessions ? (
          <div className="flex items-center justify-center py-8 text-primary">
            <Loader2 className="size-4 animate-spin" />
          </div>
        ) : sessions.length === 0 ? (
          <div className="px-4 py-8 text-center">
            <MessageSquare className="mx-auto mb-2 size-6 text-[color:var(--text-faint)]" />
            <p className="text-xs text-[color:var(--text-faint)]">
              {sessionFilter ? 'No matching conversations' : 'No conversations yet'}
            </p>
          </div>
        ) : (
          sessions.map(session => (
            <button
              key={session.id}
              type="button"
              onClick={() => onSelectSession(session)}
              className={cn(
                'group w-full rounded-[var(--radius-md)] px-3 py-2.5 text-left transition-colors',
                activeSessionId === session.id
                  ? 'bg-[color:var(--item-active)]'
                  : 'hover:bg-[color:var(--item-hover)]'
              )}
            >
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    {session.fleetKey && (
                      <Users size={12} className="shrink-0 text-primary" />
                    )}
                    <p className="truncate text-[13px] font-medium text-foreground">
                      {session.title || 'Untitled'}
                    </p>
                  </div>
                  <div className="mt-1 flex items-center gap-1.5 text-[11px] text-[color:var(--text-faint)] tabular-nums">
                    <Clock size={10} />
                    <span>{formatTimeAgo(session.updatedAt)}</span>
                    <span className="size-0.5 rounded-full bg-[color:var(--text-faint)] opacity-60" />
                    <span>
                      {session.messageCount} msg{session.messageCount !== 1 ? 's' : ''}
                    </span>
                  </div>
                </div>
                <div
                  role="button"
                  tabIndex={0}
                  onClick={(e) => onDeleteSession(e as unknown as React.MouseEvent, session.id)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      onDeleteSession(e as unknown as React.MouseEvent, session.id)
                    }
                  }}
                  className="cursor-pointer rounded p-1 opacity-0 transition-all group-hover:opacity-100 hover:bg-destructive/20"
                  title="Delete conversation"
                  aria-label={`Delete ${session.title || 'conversation'}`}
                >
                  <Trash2 size={12} className="text-destructive" />
                </div>
              </div>
            </button>
          ))
        )}
      </div>
    </div>
  )
}
