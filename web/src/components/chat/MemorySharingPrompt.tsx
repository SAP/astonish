import { useState } from 'react'
import { Users, User, Check, Loader2 } from 'lucide-react'
import { saveTeamMemory } from '../../api/platform'

/**
 * MemorySharingPrompt — shown inline after a memory_save tool result in platform mode.
 * Offers the user a quick action to share the saved memory with their team.
 *
 * Props:
 *   snippet — the memory snippet that was saved (extracted from tool args)
 *   category — the category (extracted from tool args, defaults to 'general')
 *   isPlatformMode — only render in platform mode
 */
interface MemorySharingPromptProps {
  snippet: string
  category?: string
  isPlatformMode?: boolean
}

export default function MemorySharingPrompt({ snippet, category, isPlatformMode }: MemorySharingPromptProps) {
  const [state, setState] = useState<'idle' | 'sharing' | 'shared'>('idle')

  if (!isPlatformMode || !snippet || state === 'shared') {
    if (state === 'shared') {
      return (
        <div
          className="flex items-center gap-2 px-3 py-1.5 rounded-lg text-xs mt-1"
          style={{ background: 'rgba(34,197,94,0.1)', color: '#22c55e' }}
        >
          <Check size={12} />
          <span>Shared with team</span>
        </div>
      )
    }
    return null
  }

  const handleShare = async () => {
    setState('sharing')
    try {
      await saveTeamMemory(snippet, category || 'general')
      setState('shared')
    } catch {
      setState('idle') // Silently fall back
    }
  }

  return (
    <div
      className="flex items-center gap-2 mt-1"
      style={{ fontSize: '0.75rem' }}
    >
      <span style={{ color: 'var(--text-muted)' }}>
        <User size={11} className="inline mr-1" style={{ verticalAlign: '-1px' }} />
        Saved to personal
      </span>
      <button
        onClick={handleShare}
        disabled={state === 'sharing'}
        className="flex items-center gap-1 px-2 py-0.5 rounded-md transition-colors"
        style={{
          background: 'color-mix(in oklab, var(--brand) 10%, transparent)',
          color: 'var(--brand)',
          border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)',
          cursor: state === 'sharing' ? 'wait' : 'pointer',
        }}
      >
        {state === 'sharing' ? (
          <Loader2 size={11} className="animate-spin" />
        ) : (
          <Users size={11} />
        )}
        <span>Share with team</span>
      </button>
    </div>
  )
}
