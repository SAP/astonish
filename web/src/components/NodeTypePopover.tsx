import { Edit3, Brain, Wrench, Settings, MessageSquare, X, type LucideIcon } from 'lucide-react'

/**
 * Popover showing available node types for quick creation
 */

interface NodeTypeEntry {
  type: string
  label: string
  icon: LucideIcon
  description: string
}

interface PopoverPosition {
  x: number
  y: number
}

interface NodeTypePopoverProps {
  position: PopoverPosition | null
  onSelect: (type: string) => void
  onClose: () => void
  theme: 'dark' | 'light'
}

const NODE_TYPES: NodeTypeEntry[] = [
  { type: 'input', label: 'Input', icon: Edit3, description: 'Collect user input' },
  { type: 'llm', label: 'LLM', icon: Brain, description: 'AI processing' },
  { type: 'tool', label: 'Tool', icon: Wrench, description: 'Execute a tool' },
  { type: 'output', label: 'Output', icon: MessageSquare, description: 'Show result' },
  { type: 'update_state', label: 'Update State', icon: Settings, description: 'Modify state' },
]

export default function NodeTypePopover({ 
  position, 
  onSelect, 
  onClose,
  theme 
}: NodeTypePopoverProps) {
  if (!position) return null

  return (
    <>
      {/* Backdrop to catch clicks outside */}
      <div 
        className="fixed inset-0 z-40"
        onClick={onClose}
      />
      
      {/* Popover */}
      <div 
        className="absolute z-50 w-56 rounded-xl shadow-2xl border overflow-hidden"
        style={{ 
          left: position.x,
          top: position.y,
          background: 'var(--bg-secondary)',
          borderColor: 'var(--border-color)',
          transform: 'translateX(-50%)'
        }}
      >
        {/* Header */}
        <div 
          className="flex items-center justify-between px-3 py-2 border-b"
          style={{ borderColor: 'var(--border-color)' }}
        >
          <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>
            Add Node
          </span>
          <button 
            onClick={onClose}
            className="p-1 rounded hover:bg-gray-500/20"
            style={{ color: 'var(--text-muted)' }}
          >
            <X size={12} />
          </button>
        </div>
        
        {/* Node Types */}
        <div className="py-1">
          {NODE_TYPES.map(({ type, label, icon: Icon, description }) => (
            <button
              key={type}
              onClick={() => onSelect(type)}
              className="w-full flex items-center gap-3 px-3 py-2 hover:bg-primary/10 transition-colors text-left"
            >
              <div 
                className="w-8 h-8 rounded-lg flex items-center justify-center flex-shrink-0"
                style={{ background: 'var(--bg-primary)' }}
              >
                <Icon size={16} style={{ color: 'var(--brand)' }} />
              </div>
              <div className="min-w-0">
                <div 
                  className="text-sm font-medium"
                  style={{ color: 'var(--text-primary)' }}
                >
                  {label}
                </div>
                <div 
                  className="text-xs truncate"
                  style={{ color: 'var(--text-muted)' }}
                >
                  {description}
                </div>
              </div>
            </button>
          ))}
        </div>
      </div>
    </>
  )
}
