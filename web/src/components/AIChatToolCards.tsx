import { useState } from 'react'
import { Loader2, Download, Package, Globe } from 'lucide-react'

// --- Type definitions ---

export interface ToolInfo {
  id?: string
  mcpId?: string
  name: string
  description?: string
  source?: string
  envVars?: Record<string, string>
  requiresApiKey?: boolean
  collectedEnv?: Record<string, string>
  installable?: boolean
  [key: string]: any
}

export interface InternetResult {
  name: string
  description?: string
  command?: string
  args?: string[]
  envVars?: Record<string, string>
  url?: string
  confidence?: number
  [key: string]: any
}

export interface ToolInstallCardProps {
  tool: ToolInfo
  installingTool: string | null
  onInstall: (tool: ToolInfo) => void
}

export interface StoreResultsPanelProps {
  storeResults: ToolInfo[]
  installingTool: string | null
  onInstall: (tool: ToolInfo) => void
  onSearchOnline?: (() => void) | null
}

export interface InternetResultsPanelProps {
  results: InternetResult[]
  onClear?: () => void
  onInstall: (result: InternetResult, envValues: Record<string, string>) => Promise<void>
  installingTool: string | null
}

// Component for a tool install card with optional env var configuration
export function ToolInstallCard({ tool, installingTool, onInstall }: ToolInstallCardProps) {
  const [envValues, setEnvValues] = useState<Record<string, string>>({})
  const [showConfig, setShowConfig] = useState(false)
  
  // Check if tool requires configuration
  const hasEnvVars = tool.envVars && Object.keys(tool.envVars).length > 0
  const needsConfig = hasEnvVars || tool.requiresApiKey
  
  // Format env var name for display (e.g., TAVILY_API_KEY -> "Tavily API Key")
  const formatEnvName = (envName: string) => {
    return envName
      .replace(/_/g, ' ')
      .toLowerCase()
      .replace(/\b\w/g, c => c.toUpperCase())
  }
  
  const handleInstallClick = () => {
    if (needsConfig && !showConfig) {
      // First click: show config inputs and pre-populate with defaults
      setShowConfig(true)
      // Pre-populate env values with defaults from store (curated real values)
      if (tool.envVars) {
        const defaults: Record<string, string> = {}
        Object.entries(tool.envVars).forEach(([key, defaultValue]) => {
          defaults[key] = defaultValue || ''
        })
        setEnvValues(defaults)
      }
    } else {
      // Install with only the filled env values (filter out empty ones)
      const filledEnv = Object.fromEntries(
        Object.entries(envValues).filter(([_, v]) => v?.trim())
      )
      onInstall({ ...tool, collectedEnv: filledEnv })
    }
  }
  
  const handleEnvChange = (key: string, value: string) => {
    setEnvValues(prev => ({ ...prev, [key]: value }))
  }
  
  const isInstalling = installingTool === (tool.id || tool.mcpId)
  
  return (
    <div className="bg-[var(--bg-primary)]/50 rounded-lg p-3 space-y-2">
      {/* Tool info header */}
      <div className="flex items-start justify-between gap-2">
        <div className="flex-1 min-w-0">
          <div className="font-medium text-sm text-[var(--text-primary)]">
            {tool.name}
            {needsConfig && (
              <span className="ml-2 text-xs text-yellow-400">⚙️ Config required</span>
            )}
          </div>
          <div className="text-xs text-[var(--text-secondary)] mt-0.5">
            {tool.description}
          </div>
          <div className="text-xs text-primary mt-0.5">
            Source: {tool.source}
          </div>
        </div>
        
        {!showConfig && (
          <button
            onClick={handleInstallClick}
            disabled={isInstalling}
            className="flex items-center gap-1 px-3 py-1.5 bg-primary hover:bg-primary/90 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors whitespace-nowrap"
          >
            {isInstalling ? (
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
      
      {/* Config inputs - shown when tool needs config and user clicked Configure */}
      {showConfig && hasEnvVars && (
        <div className="border-t border-white/10 pt-2 space-y-2">
          <div className="text-xs text-[var(--text-muted)]">
            Configure environment variables (optional - defaults will be used):
          </div>
          {Object.entries(tool.envVars!).map(([key, placeholder]) => (
            <div key={key} className="flex flex-col gap-1">
              <label className="text-xs text-[var(--text-secondary)]">
                {formatEnvName(key)}
              </label>
              <input
                type={key.toLowerCase().includes('key') || key.toLowerCase().includes('token') || key.toLowerCase().includes('password') || key.toLowerCase().includes('secret') ? 'password' : 'text'}
                value={envValues[key] || ''}
                onChange={(e) => handleEnvChange(key, e.target.value)}
                placeholder={(placeholder as string) || `Enter ${formatEnvName(key)}`}
                className="px-2 py-1.5 bg-[var(--bg-secondary)] border border-[var(--border-color)] rounded text-xs text-[var(--text-primary)] placeholder:text-[var(--text-muted)]"
              />
            </div>
          ))}
          
          <div className="flex gap-2 pt-1">
            <button
              onClick={() => setShowConfig(false)}
              className="flex-1 px-3 py-1.5 bg-[var(--bg-secondary)] hover:bg-[var(--bg-tertiary)] text-[var(--text-secondary)] text-xs font-medium rounded-lg transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={handleInstallClick}
              disabled={isInstalling}
              className="flex-1 flex items-center justify-center gap-1 px-3 py-1.5 bg-primary hover:bg-primary/90 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors"
            >
              {isInstalling ? (
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
          </div>
        </div>
      )}
    </div>
  )
}

// Component for the store results panel with expandable list
export function StoreResultsPanel({ storeResults, installingTool, onInstall, onSearchOnline }: StoreResultsPanelProps) {
  const [isExpanded, setIsExpanded] = useState(false)
  const INITIAL_COUNT = 5
  
  const displayedTools = isExpanded ? storeResults : storeResults.slice(0, INITIAL_COUNT)
  const hasMore = storeResults.length > INITIAL_COUNT
  
  return (
    <div className="bg-gradient-to-r from-primary/20 to-[var(--accent3)]/20 border border-primary/30 rounded-lg p-3 space-y-3">
      <div className="flex items-center gap-2 text-sm font-medium text-primary">
        <Package size={16} />
        <span>Found {storeResults.length} matching tools in store:</span>
      </div>
      <div className="space-y-3">
        {displayedTools.map((tool, idx) => (
          <ToolInstallCard 
            key={tool.id || tool.mcpId || idx}
            tool={tool}
            installingTool={installingTool}
            onInstall={onInstall}
          />
        ))}
      </div>
      {hasMore && (
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className="w-full text-xs text-primary hover:text-primary transition-colors py-1"
        >
          {isExpanded ? (
            <>▲ Show less</>
          ) : (
            <>▼ Show {storeResults.length - INITIAL_COUNT} more tools</>
          )}
        </button>
      )}
      
      {/* Always offer internet search as fallback */}
      {onSearchOnline && (
        <div className="border-t border-primary/20 pt-2 mt-2">
          <button
            onClick={onSearchOnline}
            className="w-full text-xs text-blue-400 hover:text-blue-300 transition-colors flex items-center justify-center gap-1"
          >
            <Globe size={12} />
            <span>Not what you need? Search online for more options...</span>
          </button>
        </div>
      )}
    </div>
  )
}

// Component for displaying internet search results (AI-found MCP servers)
export function InternetResultsPanel({ results, onClear, onInstall, installingTool }: InternetResultsPanelProps) {
  const [isExpanded, setIsExpanded] = useState(false)
  const [expandedResult, setExpandedResult] = useState<number | null>(null)
  const [envValues, setEnvValues] = useState<Record<string, string>>({})
  const INITIAL_COUNT = 3
  
  const displayedResults = isExpanded ? results : results.slice(0, INITIAL_COUNT)
  const hasMore = results.length > INITIAL_COUNT
  
  // Format confidence as percentage
  const formatConfidence = (conf: number) => Math.round((conf || 0) * 100) + '%'
  
  const handleInstallClick = (result: InternetResult, idx: number) => {
    const hasEnvVars = result.envVars && Object.keys(result.envVars).length > 0
    if (hasEnvVars && expandedResult !== idx) {
      // Expand to show env var inputs
      setExpandedResult(idx)
      // Initialize env values empty (envVars contains instructional placeholders, not real defaults)
      const initial: Record<string, string> = {}
      Object.keys(result.envVars!).forEach(k => initial[k] = '')
      setEnvValues(initial)
    } else {
      // Install with only the filled env vars (filter out empty ones)
      const filledEnv = Object.fromEntries(
        Object.entries(envValues).filter(([_, v]) => v?.trim())
      )
      onInstall(result, filledEnv)
    }
  }
  
  return (
    <div className="bg-gradient-to-r from-blue-600/20 to-cyan-600/20 border border-blue-500/30 rounded-lg p-3 space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium text-blue-300">
          <Globe size={16} />
          <span>Found {results.length} MCP servers online:</span>
        </div>
        <button
          onClick={onClear}
          className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)]"
        >
          ✕ Clear
        </button>
      </div>
      
      <div className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
        ⚠️ These are AI suggestions. Verify before installing.
      </div>
      
      <div className="space-y-3">
        {displayedResults.map((result, idx) => (
          <div 
            key={idx}
            className="bg-[var(--bg-primary)]/50 rounded-lg p-3 space-y-2"
          >
            <div className="flex items-start justify-between gap-2">
              <div className="flex-1 min-w-0">
                <div className="font-medium text-sm text-[var(--text-primary)]">
                  {result.name}
                  <span className="ml-2 text-xs text-blue-400">
                    {formatConfidence(result.confidence!)} match
                  </span>
                </div>
                <div className="text-xs text-[var(--text-secondary)] mt-0.5">
                  {result.description}
                </div>
              </div>
              {/* Install button - only show when NOT expanded */}
              {expandedResult !== idx && (
                <button
                  onClick={() => handleInstallClick(result, idx)}
                  disabled={installingTool === result.name}
                  className="px-3 py-1 text-xs bg-blue-600 hover:bg-blue-700 text-white rounded disabled:opacity-50 flex items-center gap-1"
                >
                  {installingTool === result.name ? (
                    <><Loader2 size={12} className="animate-spin" /> Installing...</>
                  ) : (
                    <><Download size={12} /> Install</>
                  )}
                </button>
              )}
            </div>
            
            {/* Install command preview */}
            <div className="text-xs bg-[var(--bg-secondary)] p-2 rounded font-mono text-[var(--text-muted)]">
              {result.command} {(result.args || []).join(' ')}
            </div>
            
            {/* Expanded configuration section with env vars and Cancel/Install buttons */}
            {expandedResult === idx && (
              <div className="space-y-3 pt-2 border-t border-blue-500/20">
                {result.envVars && Object.keys(result.envVars).length > 0 && (
                  <div className="space-y-2">
                    <div className="text-xs text-yellow-400">Configure environment variables (optional):</div>
                    {Object.entries(result.envVars).map(([key, placeholder]) => (
                      <div key={key} className="flex items-center gap-2">
                        <label className="text-xs text-[var(--text-secondary)] min-w-[100px]">{key}:</label>
                        <input
                          type={key.toLowerCase().includes('key') || key.toLowerCase().includes('token') || key.toLowerCase().includes('secret') ? 'password' : 'text'}
                          placeholder={placeholder as string}
                          value={envValues[key] || ''}
                          onChange={(e) => setEnvValues(prev => ({ ...prev, [key]: e.target.value }))}
                          className="flex-1 text-xs bg-[var(--bg-secondary)] border border-[var(--border-color)] rounded px-2 py-1 text-[var(--text-primary)]"
                        />
                      </div>
                    ))}
                  </div>
                )}
                
                {/* Cancel/Install buttons at bottom */}
                <div className="flex gap-2 pt-1">
                  <button
                    onClick={() => setExpandedResult(null)}
                    className="flex-1 px-3 py-1.5 bg-[var(--bg-secondary)] hover:bg-[var(--bg-tertiary)] text-[var(--text-secondary)] text-xs font-medium rounded-lg transition-colors"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={() => handleInstallClick(result, idx)}
                    disabled={installingTool === result.name}
                    className="flex-1 flex items-center justify-center gap-1 px-3 py-1.5 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors"
                  >
                    {installingTool === result.name ? (
                      <><Loader2 size={12} className="animate-spin" /> Installing...</>
                    ) : (
                      <><Download size={12} /> Install</>
                    )}
                  </button>
                </div>
              </div>
            )}
            
            {/* URL link */}
            {result.url && (
              <a 
                href={result.url} 
                target="_blank" 
                rel="noopener noreferrer"
                className="text-xs text-blue-400 hover:underline"
              >
                View on GitHub →
              </a>
            )}
          </div>
        ))}
      </div>
      
      {hasMore && (
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className="w-full text-xs text-blue-400 hover:text-blue-300 transition-colors py-1"
        >
          {isExpanded ? (
            <>▲ Show less</>
          ) : (
            <>▼ Show {results.length - INITIAL_COUNT} more</>
          )}
        </button>
      )}
    </div>
  )
}
