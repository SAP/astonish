import React, { useState, useRef } from 'react'
import type { CSSProperties } from 'react'
import { Link, ChevronRight, ChevronDown } from 'lucide-react'
import type { VariablePanelProps, HighlightedTextareaProps } from './nodeEditorTypes'

/**
 * VariablePanel - Left sidebar showing variables grouped by node
 * Inserts variables into the currently focused textarea
 */
export function VariablePanel({ variableGroups, activeTextareaRef, getValue, setValue, onVariableClick }: VariablePanelProps) {
  const [filterNode, setFilterNode] = useState('all')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  
  if (!variableGroups || variableGroups.length === 0) {
    return (
      <div className="w-48 flex-shrink-0 border-r pr-3 mr-3" style={{ borderColor: 'var(--border-color)' }}>
        <div className="text-xs font-medium mb-2" style={{ color: 'var(--text-muted)' }}>
          Variables
        </div>
        <div className="text-xs italic" style={{ color: 'var(--text-muted)' }}>
          No variables available. Add output_model to nodes to create variables.
        </div>
      </div>
    )
  }
  
  const insertVariable = (varName: string) => {
    const textarea = activeTextareaRef?.current
    const currentValue = getValue?.() || ''
    
    if (!textarea) {
      // Fallback: append to end
      setValue?.(currentValue + `{${varName}}`)
      return
    }
    
    const start = textarea.selectionStart || 0
    const end = textarea.selectionEnd || 0
    const insertion = `{${varName}}`
    const newValue = currentValue.slice(0, start) + insertion + currentValue.slice(end)
    setValue?.(newValue)
    
    // Restore cursor after insertion
    setTimeout(() => {
      textarea.focus()
      const newPos = start + insertion.length
      textarea.setSelectionRange(newPos, newPos)
    }, 0)
  }
  
  const toggleCollapse = (nodeName: string) => {
    setCollapsed(prev => ({ ...prev, [nodeName]: !prev[nodeName] }))
  }
  
  const filteredGroups = filterNode === 'all' 
    ? variableGroups 
    : variableGroups.filter(g => g.nodeName === filterNode)
  
  return (
    <div className="w-56 flex-shrink-0 border-r pr-3 mr-3 flex flex-col overflow-hidden" style={{ borderColor: 'var(--border-color)', maxHeight: '100%' }}>
      {/* Header */}
      <div className="flex items-center gap-2 mb-2">
        <Link size={12} style={{ color: 'var(--text-muted)' }} />
        <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
          Variables
        </span>
      </div>
      
      {/* Node Filter */}
      <select
        value={filterNode}
        onChange={(e) => setFilterNode(e.target.value)}
        className="w-full px-2 py-1 rounded text-xs mb-2"
        style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      >
        <option value="all">All nodes</option>
        {variableGroups.map(g => (
          <option key={g.nodeName} value={g.nodeName}>
            {g.nodeName} ({g.variables.length})
          </option>
        ))}
      </select>
      
      {/* Variable List */}
      <div className="flex-1 overflow-y-auto space-y-2">
        {filteredGroups.map(group => (
          <div key={group.nodeName}>
            {/* Node Header - only show if not filtered to single node */}
            {filterNode === 'all' && (
              <button
                onClick={() => toggleCollapse(group.nodeName)}
                className="w-full flex items-center gap-1 text-xs font-medium py-1 hover:bg-primary/10 rounded transition-colors"
                style={{ color: 'var(--text-secondary)' }}
              >
                {collapsed[group.nodeName] ? (
                  <ChevronRight size={12} />
                ) : (
                  <ChevronDown size={12} />
                )}
                <span className="truncate flex-1 text-left">{group.nodeName}</span>
                <span className="text-xs px-1 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                  {group.variables.length}
                </span>
              </button>
            )}
            
            {/* Variables */}
            {(!collapsed[group.nodeName] || filterNode !== 'all') && (
              <div className={`flex flex-wrap gap-1 ${filterNode === 'all' ? 'pl-4' : ''}`}>
                {group.variables.map(v => (
                  <button
                    key={v}
                    type="button"
                    onClick={() => onVariableClick ? onVariableClick(v) : insertVariable(v)}
                    className="px-2 py-0.5 text-xs font-mono rounded transition-all hover:scale-105 hover:bg-primary/30"
                    style={{ 
                      background: 'var(--brand-muted)', 
                      color: 'var(--brand)',
                      border: '1px solid color-mix(in oklab, var(--brand) 25%, transparent)'
                    }}
                    title={onVariableClick ? `Add {${v}} as new item` : `Insert {${v}} at cursor`}
                  >
                    {v}
                  </button>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
      
      {/* Help text */}
      <div className="text-xs mt-2 pt-2 border-t" style={{ color: 'var(--text-muted)', borderColor: 'var(--border-color)' }}>
        {onVariableClick ? 'Click to add as new item' : 'Click to insert at cursor'}
      </div>
    </div>
  )
}

/**
 * HighlightedTextarea - Textarea with variable highlighting overlay
 * Uses a transparent textarea over a highlighted pre element
 */
export const HighlightedTextarea = React.forwardRef<HTMLTextAreaElement, HighlightedTextareaProps>(function HighlightedTextarea(
  { value, onChange, onFocus, isActive, placeholder, className, style, validVariables = [] },
  ref
) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)
  
  // Get flat list of all valid variable names
  const allValidVars = validVariables.flatMap(g => g.variables || [])
  
  // Highlight {variables} in the text - only valid ones, exclude {{double braces}}
  const getHighlightedContent = () => {
    if (!value) return <span style={{ color: 'var(--text-muted)' }}>{placeholder}</span>
    
    // Match {variable} but NOT {{escaped}} - use negative lookbehind/lookahead
    // Split by single braces pattern, preserving the delimiter
    const parts = value.split(/(\{[^{}]+\})/g)
    
    return parts.map((part, i) => {
      // Check if it's a {variable} pattern (single braces only)
      const match = part.match(/^\{([^{}]+)\}$/)
      if (match) {
        const varName = match[1]
        // Only highlight if it's a valid variable
        if (allValidVars.includes(varName)) {
          return (
            <span 
              key={i}
              style={{ 
                background: 'color-mix(in oklab, var(--brand) 30%, transparent)',
                color: 'var(--brand)',
                borderRadius: '2px',
              }}
            >
              {part}
            </span>
          )
        }
      }
      return <span key={i}>{part}</span>
    })
  }
  
  const handleScroll = (e: React.UIEvent<HTMLTextAreaElement>) => {
    setScrollTop((e.target as HTMLTextAreaElement).scrollTop)
  }
  
  // Common styles for exact alignment
  const commonStyles: Record<string, string | number> = {
    fontFamily: 'ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, Liberation Mono, monospace',
    fontSize: '0.875rem',
    lineHeight: '1.25rem',
    padding: '0.5rem 0.75rem',
    margin: 0,
    border: '1px solid',
    borderRadius: '0.25rem',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-word',
    overflowWrap: 'break-word',
    letterSpacing: 'normal',
  }
  
  return (
    <div 
      ref={containerRef}
      className="relative flex-1"
      style={{ minHeight: '120px' }}
    >
      {/* Highlighted background layer */}
      <div
        className={`absolute inset-0 overflow-hidden pointer-events-none ${isActive ? 'ring-1 ring-primary' : ''}`}
        style={{ 
          ...commonStyles,
          background: 'var(--bg-primary)', 
          borderColor: isActive ? 'var(--brand-strong)' : 'var(--border-color)', 
          color: 'var(--text-primary)',
        } as CSSProperties}
      >
        <div style={{ transform: `translateY(-${scrollTop}px)` }}>
          {getHighlightedContent()}
        </div>
      </div>
      
      {/* Transparent textarea on top for editing */}
      <textarea
        ref={ref}
        value={value || ''}
        onChange={onChange}
        onFocus={onFocus}
        onScroll={handleScroll}
        className="absolute inset-0 w-full h-full resize-none"
        style={{ 
          ...commonStyles,
          background: 'transparent',
          color: 'transparent',
          caretColor: 'var(--text-primary)',
          borderColor: 'transparent',
          outline: 'none',
        } as CSSProperties}
        placeholder=""
      />
    </div>
  )
})
