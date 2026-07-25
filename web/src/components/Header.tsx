import { Play, Code, LogOut, Undo2, Redo2, CircleDot, Copy, Lock, type LucideIcon } from 'lucide-react'
import { snakeToTitleCase } from '../utils/formatters'

interface HeaderProps {
  agentName: string
  agentSource: string
  showYaml: boolean
  onToggleYaml: () => void
  isRunning: boolean
  onRun: () => void
  onExit: () => void
  canUndo: boolean
  canRedo: boolean
  onUndo: () => void
  onRedo: () => void
  readOnly: boolean
  onCopyToLocal?: () => void
}

interface ModeInfo {
  text: string
  icon: LucideIcon
  color: string
  bg: string
}

export default function Header({
  agentName, agentSource, showYaml, onToggleYaml, isRunning, onRun, onExit,
  canUndo, canRedo, onUndo, onRedo, readOnly, onCopyToLocal
}: HeaderProps) {
  let displayName: string
  if (agentSource === 'store' && agentName.includes('/')) {
    const [tap, flowName] = agentName.split('/')
    displayName = `${tap.toLowerCase()} - ${snakeToTitleCase(flowName)}`
  } else {
    displayName = snakeToTitleCase(agentName) || agentName
  }

  const getModeInfo = (): ModeInfo => {
    if (isRunning) {
      return {
        text: 'Run mode',
        icon: CircleDot,
        color: 'var(--brand)',
        bg: 'var(--item-active)',
      }
    }
    if (readOnly) {
      return {
        text: 'Read Only',
        icon: Lock,
        color: 'var(--warning)',
        bg: 'color-mix(in oklab, var(--warning) 18%, transparent)',
      }
    }
    return {
      text: 'Design mode',
      icon: CircleDot,
      color: 'var(--brand)',
      bg: 'var(--item-active)',
    }
  }
  const modeInfo = getModeInfo()
  const ModeIcon = modeInfo.icon

  return (
    <div
      className="flex h-14 items-center justify-between px-5"
      style={{
        background: 'var(--work-background, var(--bg-primary))',
        borderBottom: '1px solid var(--border-color)',
      }}
    >
      <div className="flex items-center gap-3">
        <div
          className="flex items-center gap-2 rounded-full px-3 py-1 text-xs font-semibold"
          style={{ background: modeInfo.bg, color: modeInfo.color }}
        >
          <ModeIcon size={14} />
          {modeInfo.text}
        </div>
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
            {displayName}
          </span>
          <span
            className="rounded-full px-2 py-1 text-xs"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
          >
            {agentSource === 'store' ? 'Store' : 'Local'}
          </span>
        </div>
      </div>

      <div className="flex items-center gap-2">
        {!isRunning && (
          <div className="mr-2 flex items-center gap-1">
            <button
              onClick={onUndo}
              disabled={!canUndo}
              className="rounded-full p-2 transition-colors disabled:opacity-30 hover:bg-[color:var(--item-hover)]"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
              title="Undo"
            >
              <Undo2 size={18} />
            </button>
            <button
              onClick={onRedo}
              disabled={!canRedo}
              className="rounded-full p-2 transition-colors disabled:opacity-30 hover:bg-[color:var(--item-hover)]"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
              title="Redo"
            >
              <Redo2 size={18} />
            </button>
          </div>
        )}

        {!isRunning && (
          <button
            onClick={onToggleYaml}
            className="flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium transition-colors"
            style={{
              background: showYaml ? 'var(--item-active)' : 'var(--bg-tertiary)',
              color: showYaml ? 'var(--brand)' : 'var(--text-secondary)',
              border: showYaml ? '1px solid color-mix(in oklab, var(--brand) 28%, transparent)' : '1px solid transparent',
            }}
          >
            <Code size={18} />
            {showYaml ? 'Hide Source' : 'View Source'}
          </button>
        )}

        {!isRunning && (
          <button
            onClick={onRun}
            className="send-gradient flex items-center gap-2 rounded-lg px-5 py-2 font-semibold text-white shadow-none transition-all hover:opacity-90 hover:scale-[1.02]"
          >
            <Play size={18} />
            Run
          </button>
        )}

        {!isRunning && readOnly && onCopyToLocal && (
          <button
            onClick={onCopyToLocal}
            className="flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium text-white transition-colors"
            style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--accent3) 100%)' }}
          >
            <Copy size={16} />
            Copy to Local
          </button>
        )}

        {isRunning && (
          <button
            onClick={onExit}
            className="rounded-full p-2 transition-colors hover:bg-[color:var(--item-hover)]"
            style={{ color: 'var(--text-muted)', border: '1px solid var(--border-color)' }}
            title="Exit Run Mode"
          >
            <LogOut size={20} />
          </button>
        )}
      </div>
    </div>
  )
}
