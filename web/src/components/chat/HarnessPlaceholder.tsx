import { AppWindow, ChevronRight, Clapperboard, FileText, Film, Monitor, Workflow } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { cn } from '@/lib/utils'

import type { HarnessFocus } from './chatHarness'

interface HarnessPlaceholderProps {
  focus: HarnessFocus
  title: string
  subtitle?: string
  isFocused?: boolean
  onOpen: (focus: HarnessFocus) => void
}

const ICONS: Record<HarnessFocus['kind'], LucideIcon> = {
  app: AppWindow,
  report: FileText,
  video: Film,
  distill: Workflow,
  tutorial_blueprint: Clapperboard,
  tutorial_slideshow: Clapperboard,
  browser_handoff: Monitor,
}

function kindPrefix(kind: HarnessFocus['kind']): string {
  switch (kind) {
    case 'app':
      return 'App'
    case 'report':
      return 'Report'
    case 'video':
      return 'Video'
    case 'distill':
      return 'Flow draft'
    case 'tutorial_blueprint':
      return 'Tutorial blueprint'
    case 'tutorial_slideshow':
      return 'Tutorial scenes'
    case 'browser_handoff':
      return 'Browser'
  }
}

export default function HarnessPlaceholder({
  focus,
  title,
  subtitle,
  isFocused = false,
  onOpen,
}: HarnessPlaceholderProps) {
  const Icon = ICONS[focus.kind]

  return (
    <button
      type="button"
      data-testid="harness-placeholder"
      data-harness-kind={focus.kind}
      onClick={() => onOpen(focus)}
      className={cn(
        'my-2 flex w-full max-w-xl cursor-pointer items-center gap-3 rounded-xl border px-3 py-2.5 text-left shadow-[var(--shadow-soft)] transition-colors',
        isFocused
          ? 'border-primary bg-primary/10'
          : 'border-border bg-card hover:border-primary/40'
      )}
    >
      <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/12 text-primary">
        <Icon size={16} />
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium text-foreground">
          {kindPrefix(focus.kind)}: {title}
        </div>
        {subtitle && (
          <div className="mt-0.5 truncate text-xs text-muted-foreground">
            {subtitle}
          </div>
        )}
      </div>
      <span className="flex shrink-0 items-center gap-0.5 text-xs font-medium text-primary">
        Open
        <ChevronRight size={14} />
      </span>
    </button>
  )
}
