import { useState, useMemo, useRef, useEffect } from 'react'
import { Search, ChevronDown, ChevronRight, Check, Server, Wrench, X } from 'lucide-react'

interface ToolItem {
  name: string
  description?: string
  source?: string
}

interface ToolSelectorProps {
  availableTools?: ToolItem[]
  selectedTools?: string[]
  onAddTool: (name: string) => void
  onRemoveTool: (name: string) => void
  placeholder?: string
}

/**
 * ToolSelector - Grouped, searchable tool selection component
 * 
 * Features:
 * - Tools grouped by source (MCP server)
 * - Search/filter by name or description
 * - Collapsible server groups
 * - Shows tool descriptions
 * - Keyboard navigation
 */
export default function ToolSelector({ 
  availableTools = [], 
  selectedTools = [], 
  onAddTool, 
  onRemoveTool,
  placeholder = "Search tools..."
}: ToolSelectorProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [expandedGroups, setExpandedGroups] = useState<Record<string, boolean>>({})
  const [isOpen, setIsOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement | null>(null)
  
  // Group tools by source
  const groupedTools = useMemo(() => {
    const groups: Record<string, ToolItem[]> = {}
    
    availableTools.forEach(tool => {
      const source = tool.source || 'unknown'
      if (!groups[source]) {
        groups[source] = []
      }
      groups[source].push(tool)
    })
    
    // Sort groups alphabetically, but put 'internal' first
    const sortedGroups = Object.entries(groups).sort(([a], [b]) => {
      if (a === 'internal') return -1
      if (b === 'internal') return 1
      return a.localeCompare(b)
    })
    
    return sortedGroups
  }, [availableTools])
  
  // Filter tools based on search
  const filteredGroups = useMemo(() => {
    if (!searchQuery.trim()) return groupedTools
    
    const query = searchQuery.toLowerCase()
    return groupedTools
      .map(([source, tools]): [string, ToolItem[]] => {
        const filteredTools = tools.filter(tool => 
          tool.name.toLowerCase().includes(query) ||
          (tool.description && tool.description.toLowerCase().includes(query))
        )
        return [source, filteredTools]
      })
      .filter(([, tools]) => tools.length > 0)
  }, [groupedTools, searchQuery])
  
  // Auto-expand groups when searching
  useEffect(() => {
    if (searchQuery.trim()) {
      // Expand all groups when searching
      const allExpanded: Record<string, boolean> = {}
      filteredGroups.forEach(([source]) => {
        allExpanded[source] = true
      })
      setExpandedGroups(allExpanded)
    }
  }, [searchQuery, filteredGroups])
  
  // Toggle group expansion
  const toggleGroup = (source: string) => {
    setExpandedGroups(prev => ({
      ...prev,
      [source]: !prev[source]
    }))
  }
  
  // Handle tool click
  const handleToolClick = (tool: ToolItem) => {
    if (selectedTools.includes(tool.name)) {
      onRemoveTool(tool.name)
    } else {
      onAddTool(tool.name)
    }
  }
  
  // Click outside to close
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false)
      }
    }
    
    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside)
      return () => document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen])
  
  const totalTools = availableTools.length
  const selectedCount = selectedTools.length
  
  return (
    <div ref={containerRef} className="relative">
      {/* Trigger Button */}
      <button
        type="button"
        onClick={() => setIsOpen(!isOpen)}
        className="w-full px-3 py-2 rounded border text-sm text-left flex items-start justify-between transition-colors hover:border-primary/50 min-h-[38px]"
        style={{ 
          background: 'var(--bg-primary)', 
          borderColor: isOpen ? 'color-mix(in oklab, var(--brand) 50%, transparent)' : 'var(--border-color)', 
          color: 'var(--text-primary)' 
        }}
      >
        <div className="flex items-center gap-2 flex-1 flex-wrap">
          <Wrench size={14} style={{ color: 'var(--text-muted)' }} className="flex-shrink-0 mt-0.5" />
          
          {selectedCount > 0 ? (
            <div className="flex flex-wrap gap-1.5 flex-1">
              {selectedTools.map(tool => (
                <span 
                  key={tool}
                  className="inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded border max-w-[150px] group"
                  style={{ 
                    background: 'var(--brand-muted)', 
                    color: 'var(--brand)',
                    borderColor: 'color-mix(in oklab, var(--brand-strong) 20%, transparent)'
                  }}
                  title={tool}
                >
                  <span className="truncate">{tool}</span>
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onRemoveTool(tool)
                    }}
                    className="flex-shrink-0 hover:bg-primary/30 rounded p-0.5 transition-colors"
                    title={`Remove ${tool}`}
                  >
                    <X size={10} />
                  </button>
                </span>
              ))}
            </div>
          ) : (
            <span style={{ color: 'var(--text-muted)' }}>Select tools...</span>
          )}
        </div>
        
        <ChevronDown 
          size={16} 
          className={`transition-transform flex-shrink-0 ml-2 mt-0.5 ${isOpen ? 'rotate-180' : ''}`}
          style={{ color: 'var(--text-muted)' }}
        />
      </button>
      
      {/* Dropdown Panel */}
      {isOpen && (
        <div 
          className="absolute z-50 mt-1 w-full rounded-lg border shadow-xl overflow-hidden"
          style={{ 
            background: 'var(--bg-secondary)', 
            borderColor: 'var(--border-color)',
            maxHeight: '400px'
          }}
        >
          {/* Search Input */}
          <div className="p-2 border-b" style={{ borderColor: 'var(--border-color)' }}>
            <div className="relative">
              <Search 
                size={14} 
                className="absolute left-2.5 top-1/2 -translate-y-1/2"
                style={{ color: 'var(--text-muted)' }}
              />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder={placeholder}
                autoFocus
                className="w-full pl-8 pr-3 py-1.5 rounded border text-sm"
                style={{ 
                  background: 'var(--bg-primary)', 
                  borderColor: 'var(--border-color)', 
                  color: 'var(--text-primary)' 
                }}
              />
            </div>
            <div className="flex items-center justify-between mt-1.5 px-0.5">
              <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
                {totalTools} tools available
              </span>
              {selectedCount > 0 && (
                <button
                  onClick={() => selectedTools.forEach(t => onRemoveTool(t))}
                  className="text-xs hover:underline"
                  style={{ color: 'var(--text-muted)' }}
                >
                  Clear all
                </button>
              )}
            </div>
          </div>
          
          {/* Grouped Tools List */}
          <div className="overflow-y-auto" style={{ maxHeight: '320px' }}>
            {filteredGroups.length === 0 ? (
              <div className="p-4 text-center text-sm" style={{ color: 'var(--text-muted)' }}>
                No tools found matching "{searchQuery}"
              </div>
            ) : (
              filteredGroups.map(([source, tools]) => {
                // Default to collapsed (when no search), expanded when searching
                const isExpanded = searchQuery.trim() ? expandedGroups[source] !== false : expandedGroups[source] === true
                const selectedInGroup = tools.filter(t => selectedTools.includes(t.name)).length
                
                return (
                  <div key={source} className="border-b last:border-b-0" style={{ borderColor: 'var(--border-color)' }}>
                    {/* Group Header */}
                    <button
                      onClick={() => toggleGroup(source)}
                      className="w-full px-3 py-2 flex items-center justify-between hover:bg-primary/10 transition-colors"
                      style={{ background: 'var(--bg-tertiary)' }}
                    >
                      <div className="flex items-center gap-2">
                        {isExpanded ? (
                          <ChevronDown size={14} style={{ color: 'var(--text-muted)' }} />
                        ) : (
                          <ChevronRight size={14} style={{ color: 'var(--text-muted)' }} />
                        )}
                        <Server size={14} style={{ color: 'var(--brand)' }} />
                        <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                          {source}
                        </span>
                        <span 
                          className="text-xs px-1.5 py-0.5 rounded"
                          style={{ background: 'var(--bg-primary)', color: 'var(--text-muted)' }}
                        >
                          {tools.length}
                        </span>
                      </div>
                      {selectedInGroup > 0 && (
                        <span 
                          className="text-xs px-1.5 py-0.5 rounded"
                          style={{ background: 'color-mix(in oklab, var(--brand-strong) 20%, transparent)', color: 'var(--brand)' }}
                        >
                          {selectedInGroup} selected
                        </span>
                      )}
                    </button>
                    
                    {/* Tools in Group */}
                    {isExpanded && (
                      <div className="py-1">
                        {tools.map(tool => {
                          const isSelected = selectedTools.includes(tool.name)
                          
                          return (
                            <button
                              key={tool.name}
                              onClick={() => handleToolClick(tool)}
                              className={`w-full px-3 py-2 pl-9 text-left hover:bg-primary/10 transition-colors ${
                                isSelected ? 'bg-primary/5' : ''
                              }`}
                            >
                              <div className="flex items-start gap-2">
                                <div 
                                  className={`w-4 h-4 rounded border flex-shrink-0 mt-0.5 flex items-center justify-center ${
                                    isSelected ? 'bg-primary border-primary' : ''
                                  }`}
                                  style={!isSelected ? { borderColor: 'var(--border-color)' } : {}}
                                >
                                  {isSelected && <Check size={10} className="text-white" />}
                                </div>
                                <div className="flex-1 min-w-0">
                                  <div 
                                    className="text-sm font-mono truncate"
                                    style={{ color: 'var(--text-primary)' }}
                                  >
                                    {tool.name}
                                  </div>
                                  {tool.description && (
                                    <div 
                                      className="text-xs mt-0.5 line-clamp-2"
                                      style={{ color: 'var(--text-muted)' }}
                                    >
                                      {tool.description}
                                    </div>
                                  )}
                                </div>
                              </div>
                            </button>
                          )
                        })}
                      </div>
                    )}
                  </div>
                )
              })
            )}
          </div>
        </div>
      )}
    </div>
  )
}
