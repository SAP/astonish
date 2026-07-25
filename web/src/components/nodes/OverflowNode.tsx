import { useState, type MouseEvent } from 'react'
import { Handle, Position } from '@xyflow/react'
import { AlertTriangle, Plus, type LucideIcon } from 'lucide-react'

export interface NodeData {
  label: string
  hasError?: boolean
  errorMessage?: string
  isActive?: boolean
  hasOutgoingConnection?: boolean
  [key: string]: unknown
}

interface OverflowNodeProps {
  id?: string
  data: NodeData
  selected?: boolean
  icon: LucideIcon
  nodeType: string
  hasTopHandle?: boolean
  hasBottomHandle?: boolean
  iconColor?: string
}

/**
 * Base node component with Overflow-style design
 * Uses CSS variables for theme-aware colors (light/dark)
 */
export default function OverflowNode({ 
  id,
  data, 
  selected, 
  icon: Icon, 
  nodeType,  // e.g. "LLM", "Start", etc.
  hasTopHandle = true,
  hasBottomHandle = true,
  iconColor = 'var(--node-llm)'  // Brand accent (can be overridden per node type)
}: OverflowNodeProps) {
  const [isHovered, setIsHovered] = useState(false)
  const hasError = data.hasError
  const isActive = data.isActive

  // Determine which icon to show
  const renderIcon = () => {
    if (hasError) {
      return <AlertTriangle size={20} style={{ color: 'var(--danger)' }} />
    }
    // Always show the normal icon - running state is indicated by the spinning border
    return <Icon size={20} style={{ color: iconColor }} />
  }

  const handleAddClick = (e: MouseEvent<HTMLDivElement>) => {
    e.stopPropagation()
    // Get the node's bounding rect to position the popover
    const rect = (e.currentTarget.closest('.overflow-node') as HTMLElement | null)?.getBoundingClientRect()
    if (!rect) return
    // Dispatch custom event for FlowCanvas to handle
    window.dispatchEvent(new CustomEvent('astonish:add-node-click', { 
      detail: { 
        sourceId: id, 
        position: { x: rect.left + rect.width / 2, y: rect.bottom + 10 } 
      } 
    }))
  }

  return (
    <div 
      className={`overflow-node ${isActive ? 'node-running' : ''}`}
      onMouseEnter={() => setIsHovered(true)}
      onMouseLeave={() => setIsHovered(false)}
      style={{
        background: 'var(--overflow-node-bg)',
        borderRadius: '12px',
        border: hasError 
          ? '2px solid var(--danger)' 
          : selected 
            ? '2px solid var(--primary)'
            : isActive
              ? '2px solid transparent'  // Border handled by pseudo-element
              : '1px solid var(--overflow-node-border)',
        boxShadow: hasError 
          ? '0 0 10px color-mix(in oklab, var(--danger) 40%, transparent)' 
          : selected 
            ? '0 0 0 2px var(--brand-muted), var(--shadow-soft)' 
            : isActive
              ? '0 0 20px color-mix(in oklab, var(--brand) 40%, transparent)'
              : 'var(--shadow-soft)',
        width: '180px',  // Fixed width for consistent ELK layout alignment
        padding: '14px 16px',
        position: 'relative',
        overflow: 'visible',
      }}
    >
      {/* Top Handle */}
      {hasTopHandle && (
        <Handle 
          type="target" 
          position={Position.Top} 
          className="!w-2 !h-2"
          style={{ 
            background: 'var(--overflow-handle-bg)',
            borderWidth: '2px',
            borderColor: 'var(--overflow-handle-border)',
          }}
        />
      )}
      
      {/* Content */}
      <div className="flex items-center gap-3">
        {/* Icon Container */}
        <div 
          style={{
            background: 'var(--overflow-icon-bg)',
            borderRadius: '8px',
            padding: '8px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          {renderIcon()}
        </div>
        
        {/* Text */}
        <div className="flex flex-col min-w-0">
          <span 
            style={{ 
              color: 'var(--overflow-node-title)',
              fontWeight: 600,
              fontSize: '14px',
              lineHeight: '1.3',
            }}
          >
            {nodeType}
          </span>
          <span 
            style={{ 
              color: 'var(--overflow-node-subtitle)',
              fontSize: '12px',
              lineHeight: '1.3',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {data.label}
          </span>
        </div>
      </div>
      
      {/* Error Message */}
      {hasError && data.errorMessage && (
        <p 
          className="truncate mt-2"
          style={{ 
            color: 'var(--danger)', 
            fontSize: '11px',
            maxWidth: '180px',
          }}
        >
          {data.errorMessage}
        </p>
      )}
      
      {/* Bottom Handle - styled as + button, works for both click and drag */}
      {hasBottomHandle && (() => {
        // Show expanded + button when: no connection (always) or hovered (if has connection)
        const hasConnection = data.hasOutgoingConnection
        const showExpanded = !hasConnection || isHovered
        
        return (
          <Handle 
            type="source" 
            position={Position.Bottom}
            onClick={handleAddClick}
            className="!cursor-pointer"
            style={{ 
              width: showExpanded ? '22px' : '10px',
              height: showExpanded ? '22px' : '10px',
              background: showExpanded 
                ? 'linear-gradient(135deg, var(--brand-strong) 0%, var(--brand) 100%)' 
                : 'var(--overflow-handle-bg)',
              border: showExpanded 
                ? '2px solid var(--panel-background)' 
                : '2px solid var(--overflow-handle-border)',
              borderRadius: '50%',
              transition: 'all 0.15s ease',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            {showExpanded && <Plus size={12} className="text-white pointer-events-none" />}
          </Handle>
        )
      })()}
      
      {/* Hidden handles for back-edges (loops that go upward) */}
      <Handle 
        type="source" 
        position={Position.Top} 
        id="top-source" 
        className="!opacity-0 !w-1 !h-1" 
        style={{ left: '30%' }} 
      />
      <Handle 
        type="target" 
        position={Position.Left} 
        id="left" 
        className="!opacity-0 !w-1 !h-1" 
      />
    </div>
  )
}
