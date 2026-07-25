import { useEffect, useState, type ElementType } from 'react'
import { Moon, Sun, Settings, Cpu, Grid, MessageSquare, Rocket, ShieldCheck, ShieldAlert, Crosshair, AppWindow, ChevronDown, LogOut, MoreHorizontal, Menu, User, Users } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { cn } from '@/lib/utils'

interface SandboxStatus {
  sandboxEnabled: boolean
  runtimeAvailable?: boolean
  baseTemplateExists: boolean
  // Deprecated: use runtimeAvailable
  incusAvailable?: boolean
}

interface TopBarProps {
  theme: 'dark' | 'light'
  onToggleTheme: () => void
  onOpenSandbox: () => void
  defaultProvider: string | null
  defaultModel: string | null
  currentView: string
  onNavigate?: (view: string) => void
  sandboxStatus?: SandboxStatus | null
  isPlatformMode?: boolean
  user?: { id: string, email: string, display_name: string, role: string } | null
  org?: { id: string, name: string, slug: string } | null
  orgs?: { id: string, name: string, slug: string, role: string }[] | null
  onOrgSwitch?: (orgSlug: string) => void
  activeTeam?: string | null
  teams?: { slug: string, name: string }[] | null
  onTeamChange?: (teamSlug: string) => void
  onLogout?: () => void
  personalMemoryMode?: boolean
  onTogglePersonalMemoryMode?: () => void
}

interface NavItem {
  view: string
  label: string
  Icon: ElementType<{ size?: number; className?: string }>
}

const primaryNavItems: NavItem[] = [
  { view: 'chat', label: 'Chat', Icon: MessageSquare },
  { view: 'canvas', label: 'Flows', Icon: Grid },
]

const secondaryNavItems: NavItem[] = [
  { view: 'fleet', label: 'Fleet', Icon: Rocket },
  { view: 'drill', label: 'Drill', Icon: Crosshair },
  { view: 'apps', label: 'Apps', Icon: AppWindow },
]

const allCoreNavItems: NavItem[] = [
  ...primaryNavItems,
  ...secondaryNavItems,
  { view: 'settings', label: 'Settings', Icon: Settings },
]

const BP_MD = 768
const BP_LG = 1024

function useBreakpointTier(): 'sm' | 'md' | 'lg' {
  const [tier, setTier] = useState<'sm' | 'md' | 'lg'>(() => {
    if (typeof window === 'undefined') return 'lg'
    const w = window.innerWidth
    if (w < BP_MD) return 'sm'
    if (w < BP_LG) return 'md'
    return 'lg'
  })

  useEffect(() => {
    const update = () => {
      const w = window.innerWidth
      if (w < BP_MD) setTier('sm')
      else if (w < BP_LG) setTier('md')
      else setTier('lg')
    }
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  return tier
}

export default function TopBar({ theme, onToggleTheme, onOpenSandbox, defaultProvider, defaultModel, currentView, onNavigate, sandboxStatus, isPlatformMode, user, org, orgs, onOrgSwitch, activeTeam, teams, onTeamChange, onLogout, personalMemoryMode, onTogglePersonalMemoryMode }: TopBarProps) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const tier = useBreakpointTier()
  const moreItems: NavItem[] = tier === 'md' ? secondaryNavItems : []
  const showMore = moreItems.length > 0
  const isMoreActive = moreItems.some(item => item.view === currentView)
  const activeTeamName = teams?.find(t => t.slug === activeTeam)?.name || activeTeam || 'No team'
  const initials = user?.display_name ? user.display_name.split(' ').map(w => w[0]).join('').slice(0, 2).toUpperCase() : '?'
  const runtimeOk = sandboxStatus ? (sandboxStatus.runtimeAvailable ?? sandboxStatus.incusAvailable ?? false) : false
  const sandboxSecure = Boolean(sandboxStatus?.sandboxEnabled && runtimeOk && sandboxStatus.baseTemplateExists)

  const nav = (view: string) => onNavigate?.(view)
  const mobileNav = (view: string) => {
    nav(view)
    setMobileOpen(false)
  }

  useEffect(() => {
    const onResize = () => {
      if (window.innerWidth >= BP_MD) setMobileOpen(false)
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const navButtonClass = (view: string) => cn(
    'h-auto gap-1.5 rounded-[var(--radius-md)] px-3 py-[7px] text-[13px] font-medium',
    currentView === view
      ? 'border border-[color:var(--border-soft)] bg-[color:var(--item-active)] text-foreground shadow-none hover:bg-[color:var(--item-active)]'
      : 'border border-transparent bg-transparent text-[color:var(--text-faint)] hover:bg-[color:var(--item-hover)] hover:text-foreground'
  )

  const renderInlineNavButton = (item: NavItem, extraClass = 'hidden md:inline-flex') => (
    <Button
      key={item.view}
      type="button"
      variant="ghost"
      onClick={() => nav(item.view)}
      className={cn(navButtonClass(item.view), extraClass)}
    >
      <item.Icon size={14} />
      {item.label}
    </Button>
  )

  const renderMobileNavButton = (item: NavItem) => (
    <Button
      key={item.view}
      type="button"
      variant="ghost"
      onClick={() => mobileNav(item.view)}
      className={cn('h-11 w-full justify-start gap-3 rounded-xl px-4', currentView === item.view ? 'bg-primary text-primary-foreground hover:bg-primary/90' : 'hover:bg-accent hover:text-accent-foreground')}
    >
      <item.Icon size={16} />
      {item.label}
    </Button>
  )

  return (
    <>
      <div className="relative z-50 flex h-14 items-center justify-between border-b border-border bg-[var(--shell-background)] px-4 backdrop-blur-xl">
        <div className="flex min-w-0 shrink items-center gap-3">
          <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
            <SheetTrigger asChild>
              <Button variant="ghost" size="icon" className="flex md:hidden" aria-label="Open navigation menu">
                <Menu size={20} />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="w-72 border-panel-border bg-panel-background p-0 md:hidden">
              <SheetHeader className="border-b border-border px-4 py-3 text-left">
                <SheetTitle className="flex items-center gap-2">
                  <img src="/astonish-logo.svg" alt="Astonish" className="size-6" />
                  Astonish
                </SheetTitle>
                <SheetDescription className="sr-only">Primary navigation and account controls</SheetDescription>
              </SheetHeader>

              <div className="flex-1 overflow-y-auto px-3 py-3">
                <div className="space-y-1">
                  {allCoreNavItems.map(item => renderMobileNavButton(item))}
                </div>
              </div>

              <div className="mt-auto space-y-3 border-t border-border p-3">
                {isPlatformMode && teams && teams.length > 1 && (
                  <label className="block space-y-1.5">
                    <span className="px-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Team</span>
                    <select
                      value={activeTeam || ''}
                      onChange={e => { onTeamChange?.(e.target.value); setMobileOpen(false) }}
                      className="h-9 w-full rounded-md border border-input bg-background px-3 text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {teams.map(t => <option key={t.slug} value={t.slug}>{t.name}</option>)}
                    </select>
                  </label>
                )}

                {isPlatformMode && orgs && orgs.length > 1 && (
                  <label className="block space-y-1.5">
                    <span className="px-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Organization</span>
                    <select
                      value={org?.slug || ''}
                      onChange={e => { onOrgSwitch?.(e.target.value); setMobileOpen(false) }}
                      className="h-9 w-full rounded-md border border-input bg-background px-3 text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {orgs.map(o => <option key={o.slug} value={o.slug}>{o.name}</option>)}
                    </select>
                  </label>
                )}

                <div className="flex items-center gap-2">
                  {sandboxStatus && (
                    <Button
                      type="button"
                      variant="outline"
                      size="icon"
                      onClick={() => { onOpenSandbox(); setMobileOpen(false) }}
                      title="Sandbox"
                      className={sandboxSecure ? 'text-green-500' : 'text-amber-500'}
                    >
                      {sandboxSecure ? <ShieldCheck size={18} /> : <ShieldAlert size={18} />}
                    </Button>
                  )}
                  <Button type="button" variant="outline" size="icon" onClick={onToggleTheme} title={theme === 'dark' ? 'Light mode' : 'Dark mode'}>
                    {theme === 'dark' ? <Sun size={18} className="text-yellow-400" /> : <Moon size={18} />}
                  </Button>
                </div>

                {isPlatformMode && user && (
                  <div className="flex items-center justify-between border-t border-border pt-3">
                    <div className="flex min-w-0 items-center gap-2">
                      <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">
                        {initials}
                      </div>
                      <div className="min-w-0">
                        <div className="truncate text-xs font-medium text-foreground">{user.display_name}</div>
                        <div className="truncate text-[10px] text-muted-foreground">{user.email}</div>
                      </div>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      onClick={() => { onLogout?.(); setMobileOpen(false) }}
                      className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                      title="Logout"
                    >
                      <LogOut size={16} />
                    </Button>
                  </div>
                )}
              </div>
            </SheetContent>
          </Sheet>

          <button type="button" onClick={() => nav('chat')} className="flex shrink-0 items-center gap-2.5 rounded-xl pr-2">
            <img src="/astonish-logo.svg" alt="Astonish" className="size-6" />
            <span className="hidden whitespace-nowrap text-base font-semibold text-foreground sm:inline">
              Astonish Studio
            </span>
          </button>

          {primaryNavItems.map(item => renderInlineNavButton(item, 'hidden md:inline-flex'))}
          {secondaryNavItems.map(item => renderInlineNavButton(item, 'hidden lg:inline-flex'))}

          {showMore && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" className={cn(navButtonClass('more'), 'hidden md:inline-flex lg:hidden', isMoreActive && 'bg-primary text-primary-foreground hover:bg-primary/90')}>
                  <MoreHorizontal size={14} />
                  More
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="z-[60] min-w-40">
                {moreItems.map(item => (
                  <DropdownMenuItem key={item.view} onSelect={() => nav(item.view)} className={cn(currentView === item.view && 'bg-accent text-accent-foreground')}>
                    <item.Icon size={14} />
                    {item.label}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>

        {(defaultProvider || defaultModel) && (
          <div className="hidden shrink-0 items-center gap-2 rounded-full border border-[color:var(--border-soft)] bg-[color:var(--pill-bg)] px-3 py-1.5 2xl:flex">
            <span className="size-2 rounded-full bg-primary shadow-[0_0_0_3px_var(--brand-muted)]" />
            <div className="flex items-center gap-1.5 leading-tight">
              <span className="text-[12px] font-medium text-foreground">{defaultProvider || 'Not configured'}</span>
              <span className="text-[12px] text-muted-foreground">·</span>
              <span className="font-mono text-[12px] text-muted-foreground">{defaultModel || 'No model set'}</span>
            </div>
            <Cpu size={12} className="text-[color:var(--text-faint)] opacity-60" />
          </div>
        )}

        <div className="flex shrink-0 items-center gap-2">
          {isPlatformMode && onTogglePersonalMemoryMode && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onTogglePersonalMemoryMode}
              className={cn('hidden rounded-full text-xs sm:inline-flex', personalMemoryMode ? 'border-blue-500/30 bg-blue-500/10 text-blue-500 hover:bg-blue-500/15' : 'border-primary/30 bg-primary/10 text-primary hover:bg-primary/15')}
              title={personalMemoryMode ? 'Personal mode: memories saved privately. Click to switch to team mode.' : 'Team mode: memories shared with team. Click to switch to personal mode.'}
            >
              {personalMemoryMode ? <User size={13} /> : <Users size={13} />}
              <span className="hidden xl:inline">{personalMemoryMode ? 'Personal' : 'Team'}</span>
            </Button>
          )}

          {isPlatformMode && teams && teams.length > 1 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="hidden max-w-40 rounded-full md:inline-flex">
                  <span className="truncate">{activeTeamName}</span>
                  <ChevronDown size={12} />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="z-[60] min-w-44">
                <DropdownMenuLabel>Team</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {teams.map(t => (
                  <DropdownMenuItem key={t.slug} onSelect={() => onTeamChange?.(t.slug)} className={cn(t.slug === activeTeam && 'bg-accent text-accent-foreground')}>
                    {t.name}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}

          {sandboxStatus && (
            <Button
              type="button"
              variant="outline"
              size="icon"
              onClick={onOpenSandbox}
              className={cn('hidden rounded-full sm:inline-flex', sandboxSecure ? 'text-green-500' : 'text-amber-500')}
              title={sandboxSecure ? 'Sandbox: Secure — sessions run in isolated containers' : 'Sandbox: Disabled — sessions run on host (click to configure)'}
            >
              {sandboxSecure ? <ShieldCheck size={18} /> : <ShieldAlert size={18} />}
            </Button>
          )}

          <Button type="button" variant="outline" size="icon" onClick={onToggleTheme} className="hidden rounded-full sm:inline-flex" title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}>
            {theme === 'dark' ? <Sun size={18} className="text-yellow-400" /> : <Moon size={18} />}
          </Button>

          <Button
            type="button"
            variant={currentView === 'settings' ? 'default' : 'outline'}
            size="icon"
            onClick={() => nav('settings')}
            className="hidden rounded-full sm:inline-flex"
            title="Settings"
          >
            <Settings size={18} />
          </Button>

          {isPlatformMode && user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button type="button" variant="ghost" className="hidden h-9 rounded-full px-1.5 md:inline-flex" aria-label="Open user menu">
                  <span className="flex size-8 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">
                    {initials}
                  </span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="z-[60] min-w-56">
                <DropdownMenuLabel className="space-y-1">
                  <div className="text-xs font-medium text-foreground">{user.display_name}</div>
                  <div className="text-xs font-normal text-muted-foreground">{user.email}</div>
                  <div className="flex items-center gap-2 pt-1">
                    <Badge className="text-[10px] uppercase">{user.role}</Badge>
                    {org && <span className="text-[10px] text-muted-foreground">{org.name || org.slug}</span>}
                  </div>
                </DropdownMenuLabel>
                {orgs && orgs.length > 1 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground">Organization</DropdownMenuLabel>
                    {orgs.map(o => (
                      <DropdownMenuItem key={o.slug} onSelect={() => onOrgSwitch?.(o.slug)} className={cn(o.slug === org?.slug && 'bg-accent text-accent-foreground')}>
                        <span className="flex-1">{o.name}</span>
                        <Badge variant="secondary" className="text-[10px]">{o.role}</Badge>
                      </DropdownMenuItem>
                    ))}
                  </>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={() => onLogout?.()}>
                  <LogOut size={14} />
                  Logout
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>
    </>
  )
}
