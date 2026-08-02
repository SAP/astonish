import React, { useState, useEffect, useMemo } from 'react'
import { AlertCircle, CheckCircle, ChevronRight, Clock, Loader2, Play, Search } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { teamFetch } from '../api/teamContext'

// --- Types ---

interface MCPTool {
  name: string
  description?: string
  parameters?: {
    properties?: Record<string, ParamSchema>
    [key: string]: any
  }
}

interface ParamSchema {
  type?: string
  description?: string
  [key: string]: any
}

interface MCPNetworkDenial {
  chunk_id?: string
  host: string
  port: number
  broader_pattern?: string
  rationale?: string
  security_notes?: string
}

interface MCPNetworkPreflightGrant {
  host: string
  port: number
  reason?: string
}

interface MCPNetworkAuthorization {
  required?: boolean
  message?: string
  denials?: MCPNetworkDenial[]
  hints?: MCPNetworkPreflightGrant[]
}

interface ToolRunResult {
  success: boolean
  error?: string
  result?: any
  time_taken?: string
  network_authorization?: MCPNetworkAuthorization
}

interface MCPInspectorProps {
  serverName: string
  teamSlug?: string
  scope?: string  // 'team' | 'platform' | undefined (org default)
  onClose: () => void
}

// Fetch tools for a specific MCP server
const fetchServerTools = async (serverName: string, teamSlug?: string, scope?: string): Promise<{ tools?: MCPTool[]; error?: string; network_authorization?: MCPNetworkAuthorization }> => {
  const scopeParam = scope || (teamSlug ? 'team' : '')
  const scopeQuery = scopeParam ? `?scope=${scopeParam}` : ''
  const res = await teamFetch(`/api/mcp/${encodeURIComponent(serverName)}/tools${scopeQuery}`, undefined, teamSlug)
  if (!res.ok) throw new Error('Failed to fetch tools')
  return res.json()
}

// Run a tool on a specific MCP server
const runServerTool = async (serverName: string, toolName: string, params: Record<string, any>, teamSlug?: string, scope?: string): Promise<ToolRunResult> => {
  const scopeParam = scope || (teamSlug ? 'team' : '')
  const scopeQuery = scopeParam ? `?scope=${scopeParam}` : ''
  const res = await teamFetch(`/api/mcp/${encodeURIComponent(serverName)}/tools/${encodeURIComponent(toolName)}/run${scopeQuery}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ params })
  }, teamSlug)
  if (!res.ok) throw new Error('Failed to run tool')
  return res.json()
}

const grantMCPNetworkAccess = async (serverName: string, denial: { host: string; port?: number }, teamSlug?: string, scope?: string): Promise<void> => {
  const scopeParam = scope || (teamSlug ? 'team' : '')
  const scopeQuery = scopeParam ? `?scope=${scopeParam}` : ''
  const res = await teamFetch(`/api/mcp/${encodeURIComponent(serverName)}/network-grants${scopeQuery}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ host: denial.host, port: denial.port || 443 })
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to grant network access')
  }
}

export default function MCPInspector({ serverName, teamSlug, scope, onClose }: MCPInspectorProps) {
  const [tools, setTools] = useState<MCPTool[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [networkAuthorization, setNetworkAuthorization] = useState<MCPNetworkAuthorization | null>(null)
  const [grantingNetwork, setGrantingNetwork] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedTool, setSelectedTool] = useState<MCPTool | null>(null)
  const [params, setParams] = useState<Record<string, any>>({})
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<ToolRunResult | null>(null)
  const [resultError, setResultError] = useState<string | null>(null)

  const loadTools = async () => {
    setLoading(true)
    setError(null)
    setNetworkAuthorization(null)
    try {
      const data = await fetchServerTools(serverName, teamSlug, scope)
      if (data.error) {
        setError(data.error)
        setNetworkAuthorization(data.network_authorization || null)
      } else {
        setTools(data.tools || [])
        if (data.tools?.length && data.tools.length > 0) {
          setSelectedTool(data.tools[0])
        }
      }
    } catch (err: any) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadTools()
  }, [serverName, teamSlug, scope])

  const filteredTools = useMemo(() => {
    if (!searchQuery) return tools
    const query = searchQuery.toLowerCase()
    return tools.filter(t =>
      t.name.toLowerCase().includes(query) ||
      (t.description && t.description.toLowerCase().includes(query))
    )
  }, [tools, searchQuery])

  useEffect(() => {
    setParams({})
    setResult(null)
    setResultError(null)
  }, [selectedTool])

  const handleRun = async () => {
    if (!selectedTool) return
    setRunning(true)
    setResult(null)
    setResultError(null)
    setNetworkAuthorization(null)
    try {
      const res = await runServerTool(serverName, selectedTool.name, params, teamSlug, scope)
      if (res.success) {
        setResult(res)
      } else {
        setResultError(res.error || 'Unknown error')
        setNetworkAuthorization(res.network_authorization || null)
        setResult(res)
      }
    } catch (err: any) {
      setResultError(err.message)
    } finally {
      setRunning(false)
    }
  }

  const handleGrantAndRetry = async (denial: { host: string; port?: number }, retry: 'load-tools' | 'run-tool') => {
    const key = `${denial.host}:${denial.port || 443}`
    setGrantingNetwork(key)
    try {
      await grantMCPNetworkAccess(serverName, denial, teamSlug, scope)
      if (retry === 'run-tool') {
        await handleRun()
      } else {
        await loadTools()
      }
    } catch (err: any) {
      if (retry === 'run-tool') {
        setResultError(err.message || 'Failed to grant network access')
      } else {
        setError(err.message || 'Failed to grant network access')
      }
    } finally {
      setGrantingNetwork(null)
    }
  }

  const networkTargets = useMemo(() => {
    if (!networkAuthorization?.required) return []
    const seen = new Set<string>()
    const targets: Array<{ host: string; port: number; reason?: string; broader_pattern?: string }> = []
    for (const denial of networkAuthorization.denials || []) {
      if (!denial.host) continue
      const port = denial.port || 443
      const key = `${denial.host}:${port}`
      if (seen.has(key)) continue
      seen.add(key)
      targets.push({ host: denial.host, port, broader_pattern: denial.broader_pattern })
    }
    for (const hint of networkAuthorization.hints || []) {
      if (!hint.host) continue
      const port = hint.port || 443
      const key = `${hint.host}:${port}`
      if (seen.has(key)) continue
      seen.add(key)
      targets.push({ host: hint.host, port, reason: hint.reason })
    }
    return targets
  }, [networkAuthorization])

  const renderNetworkAuthorization = (retry: 'load-tools' | 'run-tool') => {
    if (!networkAuthorization?.required || networkTargets.length === 0) return null
    return (
      <Alert className="max-w-3xl border-amber-200 bg-amber-50 text-amber-950 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-100">
        <AlertCircle className="text-amber-600 dark:text-amber-400" />
        <AlertDescription className="space-y-3">
          <div>
            <p className="font-medium">Outbound network access is required</p>
            <p className="mt-1 text-sm">
              {networkAuthorization.message || 'This MCP server needs permission to reach external hosts before Astonish can install or start it.'}
            </p>
          </div>
          <div className="space-y-2">
            {networkTargets.map((target) => {
              const key = `${target.host}:${target.port || 443}`
              const isGranting = grantingNetwork === key
              return (
                <div key={key} className="flex flex-col gap-2 rounded-lg border border-amber-200/80 bg-background/80 p-3 text-left dark:border-amber-900/60 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0">
                    <p className="break-all font-mono text-sm text-foreground">{target.host}:{target.port || 443}</p>
                    {target.reason && <p className="mt-1 text-xs text-muted-foreground">{target.reason}</p>}
                    {target.broader_pattern && target.broader_pattern !== target.host && (
                      <p className="mt-1 text-xs text-muted-foreground">Suggested broader pattern: <span className="font-mono">{target.broader_pattern}</span></p>
                    )}
                  </div>
                  <Button size="sm" onClick={() => handleGrantAndRetry(target, retry)} disabled={Boolean(grantingNetwork)}>
                    {isGranting && <Loader2 className="animate-spin" />}
                    {isGranting ? 'Granting...' : 'Grant access and retry'}
                  </Button>
                </div>
              )
            })}
          </div>
          <p className="text-xs text-muted-foreground">
            Astonish saves this as a durable allow rule for the selected MCP scope, then retries in a fresh sandbox.
          </p>
        </AlertDescription>
      </Alert>
    )
  }

  const renderParamField = (name: string, schema: ParamSchema = {}) => {
    const type = schema.type || 'string'
    const value = params[name] ?? ''

    return (
      <div key={name} className="space-y-2">
        <Label htmlFor={`mcp-param-${name}`}>
          {name}
          {schema.description && (
            <span className="ml-1 font-normal text-muted-foreground">- {schema.description}</span>
          )}
        </Label>
        {type === 'boolean' ? (
          <Select
            value={value === true ? 'true' : value === false ? 'false' : '__unset__'}
            onValueChange={(next) => setParams({ ...params, [name]: next === 'true' })}
          >
            <SelectTrigger id={`mcp-param-${name}`} className="bg-background">
              <SelectValue placeholder="Select..." />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__unset__">Select...</SelectItem>
              <SelectItem value="true">true</SelectItem>
              <SelectItem value="false">false</SelectItem>
            </SelectContent>
          </Select>
        ) : type === 'array' || type === 'object' ? (
          <Textarea
            id={`mcp-param-${name}`}
            value={typeof value === 'object' ? JSON.stringify(value, null, 2) : value}
            onChange={(e) => {
              try {
                setParams({ ...params, [name]: JSON.parse(e.target.value) })
              } catch {
                setParams({ ...params, [name]: e.target.value })
              }
            }}
            placeholder={`Enter ${type === 'array' ? 'array' : 'object'} as JSON...`}
            rows={3}
            className="bg-background font-mono"
          />
        ) : type === 'number' || type === 'integer' ? (
          <Input
            id={`mcp-param-${name}`}
            type="number"
            value={value}
            onChange={(e) => setParams({ ...params, [name]: e.target.valueAsNumber || '' })}
            placeholder={`Enter ${name}...`}
            className="bg-background"
          />
        ) : (
          <Input
            id={`mcp-param-${name}`}
            type="text"
            value={value}
            onChange={(e) => setParams({ ...params, [name]: e.target.value })}
            placeholder={`Enter ${name}...`}
            className="bg-background"
          />
        )}
      </div>
    )
  }

  const getToolParams = (tool: MCPTool | null): Record<string, ParamSchema> => {
    if (!tool?.parameters) return {}
    const params = tool.parameters
    if (params.properties) return params.properties
    if (typeof params === 'object') return params as Record<string, ParamSchema>
    return {}
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className="flex h-[80vh] w-full max-w-6xl flex-col gap-0 overflow-hidden border-panel-border bg-panel-background p-0 shadow-[var(--shadow-elevated)] sm:max-w-6xl"
        showCloseButton
      >
        <DialogHeader className="border-b px-4 py-4 text-left">
          <DialogTitle>Tool Inspector</DialogTitle>
          <DialogDescription>{serverName}</DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="flex flex-1 items-center justify-center gap-2 p-8 text-sm text-muted-foreground">
            <Loader2 className="size-6 animate-spin text-primary" />
            Loading tools...
          </div>
        ) : error ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-4 overflow-y-auto p-8 text-center">
            <AlertCircle className="size-12 text-destructive" />
            <div>
              <p className="text-lg font-medium text-foreground">Failed to load tools</p>
              <p className="mt-2 max-w-3xl text-sm text-muted-foreground">{error}</p>
            </div>
            {renderNetworkAuthorization('load-tools')}
          </div>
        ) : (
          <div className="flex min-h-0 flex-1 overflow-hidden">
            <div className="flex w-72 shrink-0 flex-col border-r bg-background/40">
              <div className="border-b p-3">
                <div className="relative">
                  <Search className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    type="text"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder="Search tools..."
                    className="bg-background pl-9"
                  />
                </div>
              </div>

              <div className="min-h-0 flex-1 space-y-1 overflow-y-auto p-2">
                {filteredTools.length === 0 ? (
                  <p className="p-4 text-center text-sm text-muted-foreground">
                    {searchQuery ? 'No tools match your search' : 'No tools available'}
                  </p>
                ) : (
                  filteredTools.map(tool => (
                    <button
                      key={tool.name}
                      type="button"
                      onClick={() => setSelectedTool(tool)}
                      className={cn(
                        'w-full rounded-lg px-3 py-2 text-left transition-all',
                        selectedTool?.name === tool.name
                          ? 'bg-primary/10 text-primary ring-1 ring-primary/30'
                          : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground'
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <ChevronRight className={cn('size-3.5', selectedTool?.name === tool.name ? 'opacity-100' : 'opacity-0')} />
                        <span className="truncate text-sm font-medium">{tool.name}</span>
                      </div>
                    </button>
                  ))
                )}
              </div>

              <div className="border-t p-2 text-center text-xs text-muted-foreground">
                {tools.length} tools available
              </div>
            </div>

            <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
              {selectedTool ? (
                <>
                  <div className="border-b p-4">
                    <h3 className="text-lg font-semibold text-foreground">{selectedTool.name}</h3>
                    {selectedTool.description && (
                      <p className="mt-1 text-sm text-muted-foreground">{selectedTool.description}</p>
                    )}
                  </div>

                  <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
                    <h4 className="text-sm font-medium text-foreground">Parameters</h4>
                    {Object.keys(getToolParams(selectedTool)).length === 0 ? (
                      <p className="text-sm text-muted-foreground">This tool has no parameters</p>
                    ) : (
                      Object.entries(getToolParams(selectedTool)).map(([name, schema]) =>
                        renderParamField(name, schema)
                      )
                    )}

                    <Button onClick={handleRun} disabled={running}>
                      {running ? <Loader2 className="animate-spin" /> : <Play />}
                      {running ? 'Running...' : 'Run Tool'}
                    </Button>

                    {result && (
                      <div className="mt-4 space-y-2">
                        <div className="flex items-center gap-2">
                          {result.success ? (
                            <CheckCircle className="size-4 text-[color:var(--success)]" />
                          ) : (
                            <AlertCircle className="size-4 text-destructive" />
                          )}
                          <span className={cn('text-sm font-medium', result.success ? 'text-[color:var(--success)]' : 'text-destructive')}>
                            {result.success ? 'Success' : 'Error'}
                          </span>
                          {result.time_taken && (
                            <span className="flex items-center gap-1 text-xs text-muted-foreground">
                              <Clock className="size-3" />
                              {result.time_taken}
                            </span>
                          )}
                        </div>

                        {resultError && (
                          <Alert variant="destructive">
                            <AlertCircle />
                            <AlertDescription>{resultError}</AlertDescription>
                          </Alert>
                        )}

                        {renderNetworkAuthorization('run-tool')}

                        {result.result && (
                          <pre className="overflow-x-auto rounded-lg border bg-background p-3 font-mono text-sm text-foreground">
                            {JSON.stringify(result.result, null, 2)}
                          </pre>
                        )}
                      </div>
                    )}
                  </div>
                </>
              ) : (
                <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                  Select a tool to inspect
                </div>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
