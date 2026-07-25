import { useState } from 'react'
import { AlertTriangle, Check, Download, Server, X, Loader2, ChevronDown, ChevronUp } from 'lucide-react'
import type { McpDependency, McpDependencyCheckResult } from '../api/agents'

/**
 * MCPDependenciesPanel - Shows MCP server dependencies status for a flow
 * Displays which servers are installed vs missing, with install buttons
 */

interface MCPDependenciesPanelProps {
  dependencies: McpDependencyCheckResult
  onInstall: (dep: McpDependency) => void
  onDismiss: () => void
  isInstalling?: string | null
}

export default function MCPDependenciesPanel({ 
  dependencies, 
  onInstall,
  onDismiss,
  isInstalling = null
}: MCPDependenciesPanelProps) {
  const [expanded, setExpanded] = useState(true)
  
  if (!dependencies || dependencies.dependencies.length === 0) {
    return null
  }
  
  const { dependencies: deps, all_installed, missing } = dependencies
  
  // If all installed, show minimal success state
  if (all_installed) {
    return null // Don't show anything if all installed
  }
  
  return (
    <div 
      className="mx-4 my-2 rounded-lg border overflow-hidden"
      style={{ 
        background: 'var(--bg-secondary)',
        borderColor: 'rgba(251, 191, 36, 0.3)'
      }}
    >
      {/* Header - always visible */}
      <div
        role="button"
        tabIndex={0}
        onClick={() => setExpanded(!expanded)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            setExpanded(!expanded)
          }
        }}
        className="w-full px-4 py-3 flex items-center justify-between hover:bg-yellow-500/5 transition-colors cursor-pointer select-none"
        style={{ background: 'rgba(251, 191, 36, 0.1)' }}
      >
        <div className="flex items-center gap-3">
          <AlertTriangle size={18} className="text-yellow-500" />
          <span className="font-medium" style={{ color: 'var(--text-primary)' }}>
            {missing} Missing MCP Server{missing > 1 ? 's' : ''}
          </span>
        </div>
        <div className="flex items-center gap-2">
          {onDismiss && (
            <button
              onClick={(e) => {
                e.stopPropagation()
                onDismiss()
              }}
              className="p-1 rounded hover:bg-gray-500/20 transition-colors"
              title="Dismiss"
            >
              <X size={16} style={{ color: 'var(--text-muted)' }} />
            </button>
          )}
          {expanded ? (
            <ChevronUp size={18} style={{ color: 'var(--text-muted)' }} />
          ) : (
            <ChevronDown size={18} style={{ color: 'var(--text-muted)' }} />
          )}
        </div>
      </div>
      
      {/* Expandable content */}
      {expanded && (
        <div className="px-4 py-3 space-y-2 border-t" style={{ borderColor: 'rgba(251, 191, 36, 0.2)' }}>
          <p className="text-sm mb-3" style={{ color: 'var(--text-muted)' }}>
            This flow requires MCP servers that are not installed. Install them to use all features.
          </p>
          
          {deps.map((dep) => (
            <div 
              key={dep.server}
              className="flex items-center justify-between p-3 rounded-lg border"
              style={{ 
                background: 'var(--bg-primary)',
                borderColor: dep.installed ? 'rgba(34, 197, 94, 0.3)' : 'var(--border-color)'
              }}
            >
              <div className="flex items-center gap-3">
                <div 
                  className="w-8 h-8 rounded-lg flex items-center justify-center"
                  style={{ 
                    background: dep.installed 
                      ? 'rgba(34, 197, 94, 0.15)' 
                      : 'rgba(251, 191, 36, 0.15)',
                    border: `1px solid ${dep.installed ? 'rgba(34, 197, 94, 0.3)' : 'rgba(251, 191, 36, 0.3)'}`
                  }}
                >
                  <Server size={14} style={{ color: dep.installed ? '#22c55e' : '#fbbf24' }} />
                </div>
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm" style={{ color: 'var(--text-primary)' }}>
                      {dep.server}
                    </span>
                    <span 
                      className="text-xs px-1.5 py-0.5 rounded"
                      style={{ 
                        background: dep.source === 'store' 
                          ? 'var(--brand-muted)' 
                          : dep.source === 'tap' 
                            ? 'rgba(59, 130, 246, 0.15)'
                            : 'rgba(107, 114, 128, 0.15)',
                        color: dep.source === 'store' 
                          ? 'var(--brand)' 
                          : dep.source === 'tap' 
                            ? '#3b82f6'
                            : '#6b7280'
                      }}
                    >
                      {dep.source}
                    </span>
                  </div>
                  {dep.tools && dep.tools.length > 0 && (
                    <div className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>
                      Tools: {dep.tools.slice(0, 3).join(', ')}{dep.tools.length > 3 ? ` +${dep.tools.length - 3}` : ''}
                    </div>
                  )}
                </div>
              </div>
              
              <div className="flex items-center gap-2">
                {dep.installed ? (
                  <span className="flex items-center gap-1 text-xs px-2 py-1 rounded" 
                    style={{ background: 'rgba(34, 197, 94, 0.15)', color: '#22c55e' }}>
                    <Check size={12} />
                    Installed
                  </span>
                ) : (
                  <button
                    onClick={() => onInstall && onInstall(dep)}
                    disabled={isInstalling === dep.server}
                    className="flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium transition-all hover:scale-105 disabled:opacity-50 disabled:cursor-not-allowed"
                    style={{ 
                      background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)',
                      color: 'white'
                    }}
                  >
                    {isInstalling === dep.server ? (
                      <>
                        <Loader2 size={12} className="animate-spin" />
                        Installing...
                      </>
                    ) : (
                      <>
                        <Download size={12} />
                        Install
                      </>
                    )}
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
