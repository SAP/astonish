import { useState, useMemo, type ReactNode } from 'react'
import { Plus, Trash2, Store, Search, ChevronDown, ChevronRight, FolderOpen, Upload, GitFork, User, Users } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { snakeToTitleCase } from '../utils/formatters'

interface Agent {
  id: string
  name: string
  description?: string
  source: string
  scope?: string // "personal" | "team" (platform mode only)
  tapName?: string
}

interface SidebarProps {
  agents: Agent[]
  selectedAgent: Agent | null
  onAgentSelect: (agent: Agent) => void
  onCreateNew: () => void
  onDeleteAgent: (agent: Agent) => void
  onPublishFlow?: (agent: Agent) => void
  onForkFlow?: (agent: Agent) => void
  isLoading: boolean
}

interface GroupedAgents {
  personal: Agent[]
  team: Agent[]
  local: Agent[]
  official: Agent[]
  taps: Record<string, Agent[]>
}

export default function Sidebar({
  agents,
  selectedAgent,
  onAgentSelect,
  onCreateNew,
  onDeleteAgent,
  onPublishFlow,
  onForkFlow,
  isLoading,
}: SidebarProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [sourceFilter, setSourceFilter] = useState('all') // 'all', 'local', 'official', or tap name
  const [collapsedSections, setCollapsedSections] = useState<Record<string, boolean>>({})

  const isPlatformMode = useMemo(() => agents.some(a => a.scope), [agents])

  const sources = useMemo(() => {
    const srcSet = new Set<string>()
    agents.forEach(a => {
      if (a.scope === 'personal') srcSet.add('personal')
      else if (a.scope === 'team') srcSet.add('team')
      else if (a.source === 'store') srcSet.add(a.tapName || 'official')
      else srcSet.add('local')
    })
    return Array.from(srcSet)
  }, [agents])

  const groupedAgents = useMemo((): GroupedAgents => {
    let filtered = agents.filter(a =>
      a.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      (a.description && a.description.toLowerCase().includes(searchQuery.toLowerCase()))
    )

    if (sourceFilter !== 'all') {
      filtered = filtered.filter(a => {
        if (sourceFilter === 'personal') return a.scope === 'personal'
        if (sourceFilter === 'team') return a.scope === 'team'
        if (sourceFilter === 'local') return !a.scope && a.source !== 'store'
        if (sourceFilter === 'official') return a.source === 'store' && a.tapName === 'official'
        return a.source === 'store' && a.tapName === sourceFilter
      })
    }

    const groups: GroupedAgents = {
      personal: [],
      team: [],
      local: [],
      official: [],
      taps: {},
    }

    filtered.forEach(a => {
      if (a.scope === 'personal') {
        groups.personal.push(a)
      } else if (a.scope === 'team') {
        groups.team.push(a)
      } else if (a.source !== 'store') {
        groups.local.push(a)
      } else if (a.tapName === 'official') {
        groups.official.push(a)
      } else {
        const tap = a.tapName || 'unknown'
        if (!groups.taps[tap]) groups.taps[tap] = []
        groups.taps[tap].push(a)
      }
    })

    return groups
  }, [agents, searchQuery, sourceFilter])

  const toggleSection = (section: string) => {
    setCollapsedSections(prev => ({
      ...prev,
      [section]: !prev[section],
    }))
  }

  const renderAgentList = (agentList: Agent[]): ReactNode => (
    <div className="space-y-1">
      {agentList.map((agent) => {
        const isSelected = selectedAgent?.id === agent.id
        return (
          <div
            key={agent.id}
            className={cn(
              'group flex items-center gap-1 rounded-[var(--radius-md)] border px-2 py-2 transition-colors',
              isSelected
                ? 'bg-[color:var(--item-active)]'
                : 'border-transparent hover:bg-[color:var(--item-hover)]'
            )}
            style={isSelected ? {
              borderColor: 'color-mix(in oklab, var(--brand) 28%, transparent)',
            } : undefined}
          >
            <button
              onClick={() => onAgentSelect(agent)}
              className="min-w-0 flex-1 text-left"
              title={agent.description || ''}
            >
              <div className="truncate text-sm font-medium text-foreground">
                {snakeToTitleCase(agent.name.includes('/') ? agent.name.split('/').pop()! : agent.name)}
              </div>
              {agent.description && (
                <div className="mt-0.5 max-w-40 truncate text-xs text-[color:var(--text-faint,var(--text-muted))]">
                  {agent.description}
                </div>
              )}
            </button>

            {agent.scope === 'personal' && onPublishFlow && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={(e: React.MouseEvent) => {
                  e.stopPropagation()
                  onPublishFlow(agent)
                }}
                className="size-7 shrink-0 text-primary opacity-0 transition-all hover:bg-[color:var(--item-active)] group-hover:opacity-100"
                title="Publish to Team"
              >
                <Upload size={14} />
              </Button>
            )}
            {agent.scope === 'team' && onForkFlow && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={(e: React.MouseEvent) => {
                  e.stopPropagation()
                  onForkFlow(agent)
                }}
                className="size-7 shrink-0 text-[color:var(--success,#22c55e)] opacity-0 transition-all hover:bg-green-500/10 group-hover:opacity-100"
                title="Fork to Personal"
              >
                <GitFork size={14} />
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={(e: React.MouseEvent) => {
                e.stopPropagation()
                onDeleteAgent(agent)
              }}
              className="size-7 shrink-0 text-destructive opacity-0 transition-all hover:bg-destructive/10 hover:text-destructive group-hover:opacity-100"
              title={agent.source === 'store' ? 'Uninstall flow' : 'Delete agent'}
            >
              <Trash2 size={14} />
            </Button>
          </div>
        )
      })}
    </div>
  )

  const renderSection = (title: string, agents: Agent[], icon: ReactNode, sectionKey: string, colorClass = 'text-muted-foreground'): ReactNode => {
    if (agents.length === 0) return null
    const isCollapsed = collapsedSections[sectionKey]

    return (
      <div className="mb-2">
        <button
          onClick={() => toggleSection(sectionKey)}
          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-xs font-semibold tracking-wide text-[color:var(--text-faint,var(--text-muted))] uppercase transition-colors hover:bg-[color:var(--item-hover)] hover:text-foreground"
        >
          {isCollapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
          <span className={colorClass}>{icon}</span>
          <span>{title}</span>
          <span className="ml-auto rounded-full bg-[color:var(--bg-tertiary)] px-1.5 py-0.5 text-[10px] text-[color:var(--text-faint,var(--text-muted))]">{agents.length}</span>
        </button>
        {!isCollapsed && renderAgentList(agents)}
      </div>
    )
  }

  return (
    <div className="flex w-64 flex-col border-r border-border bg-[color:var(--work-sidebar,var(--sidebar-background))]">
      <div className="border-b border-border p-3">
        <Button onClick={onCreateNew} className="h-10 w-full gap-2 rounded-xl font-semibold shadow-none">
          <Plus size={18} />
          New Flow
        </Button>
      </div>

      <div className="space-y-2 border-b border-border p-2">
        <div className="relative">
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[color:var(--text-faint,var(--text-muted))]" />
          <Input
            type="text"
            placeholder="Search flows"
            value={searchQuery}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSearchQuery(e.target.value)}
            className="border-[color:var(--border-soft,var(--border))] bg-[color:var(--search-bg,var(--bg-tertiary))] pl-8 text-sm shadow-none"
          />
        </div>

        <Select value={sourceFilter} onValueChange={setSourceFilter}>
          <SelectTrigger className="w-full border-[color:var(--border-soft,var(--border))] bg-[color:var(--search-bg,var(--bg-tertiary))] text-sm text-foreground shadow-none">
            <SelectValue placeholder="All Sources" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Sources</SelectItem>
            {isPlatformMode && <SelectItem value="personal">Personal</SelectItem>}
            {isPlatformMode && <SelectItem value="team">Team</SelectItem>}
            {!isPlatformMode && <SelectItem value="local">Local</SelectItem>}
            <SelectItem value="official">Official Store</SelectItem>
            {sources.filter(s => !['local', 'official', 'personal', 'team'].includes(s)).map(tap => (
              <SelectItem key={tap} value={tap}>{tap}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="flex-1 space-y-3 overflow-y-auto p-2">
        {isLoading ? (
          <div className="space-y-2 py-4">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-11/12" />
            <Skeleton className="h-8 w-10/12" />
          </div>
        ) : (
          <>
            {isPlatformMode && renderSection('Personal', groupedAgents.personal, <User size={12} />, 'personal', 'text-primary')}
            {isPlatformMode && renderSection('Team', groupedAgents.team, <Users size={12} />, 'team', 'text-primary')}
            {!isPlatformMode && renderSection('Local', groupedAgents.local, <FolderOpen size={12} />, 'local', 'text-primary')}
            {renderSection('Official Store', groupedAgents.official, <Store size={12} />, 'official', 'text-primary')}

            {Object.entries(groupedAgents.taps).map(([tapName, tapAgents]) => (
              <div key={`tap-${tapName}`}>
                {renderSection(tapName, tapAgents, <Store size={12} />, `tap-${tapName}`)}
              </div>
            ))}

            {groupedAgents.personal.length === 0 &&
             groupedAgents.team.length === 0 &&
             groupedAgents.local.length === 0 &&
             groupedAgents.official.length === 0 &&
             Object.keys(groupedAgents.taps).length === 0 && (
              <div className="rounded-lg border border-dashed border-border px-3 py-8 text-center text-sm text-muted-foreground">
                {searchQuery ? 'No flows match your search' : 'No flows available'}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
