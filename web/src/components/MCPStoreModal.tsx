import { useState, useEffect, useCallback, useRef } from 'react'
import { AlertCircle, Check, Download, ExternalLink, Loader2, Package, Search, Star } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { cn } from '@/lib/utils'

import { teamFetch } from '../api/teamContext'
import CredentialBindControl from '@/components/credentials/CredentialBindControl'
import { isSensitiveEnvKey, omitEmptySensitiveEnv } from '@/components/credentials/credentialPlaceholders'

// --- Types ---

interface MCPServerConfig {
  command?: string
  env?: Record<string, string>
  [key: string]: unknown
}

interface MCPServer {
  mcpId: string
  name: string
  author: string
  description: string
  githubStars: number
  githubUrl: string
  tags?: string[]
  config?: MCPServerConfig
}

interface MCPStoreModalProps {
  isOpen: boolean
  onClose: () => void
  onInstall?: (server: MCPServer) => void
  teamSlug?: string
  scope?: string  // 'team' | 'platform' | undefined (org default)
}

const installMCPServer = async (mcpId: string, env: Record<string, string> = {}, teamSlug?: string, scope?: string): Promise<Record<string, unknown>> => {
  const encodedId = encodeURIComponent(mcpId).replace(/%2F/g, '/')
  const scopeParam = scope || (teamSlug ? 'team' : '')
  const url = scopeParam
    ? `/api/mcp-store/${encodedId}/install?scope=${scopeParam}`
    : `/api/mcp-store/${encodedId}/install`
  const res = await teamFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ env })
  }, teamSlug)
  if (!res.ok) {
    const errorText = await res.text()
    throw new Error(errorText || `Failed to install MCP server (${res.status})`)
  }
  return res.json()
}

export default function MCPStoreModal({ isOpen, onClose, onInstall, teamSlug, scope }: MCPStoreModalProps) {
  const [servers, setServers] = useState<MCPServer[]>([])
  const [sources, setSources] = useState<string[]>([])
  const [selectedSource, setSelectedSource] = useState('all')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const searchDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [selectedServer, setSelectedServer] = useState<MCPServer | null>(null)
  const [installing, setInstalling] = useState<string | null>(null)
  const [installSuccess, setInstallSuccess] = useState<string | null>(null)
  const [envOverrides, setEnvOverrides] = useState<Record<string, string>>({})

  const loadServers = useCallback(async (query = '', source = 'all') => {
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams()
      if (query) params.set('q', query)
      if (source && source !== 'all') params.set('source', source)
      const url = params.toString() ? `/api/mcp-store?${params}` : '/api/mcp-store'

      const res = await teamFetch(url)
      if (!res.ok) throw new Error('Failed to fetch MCP store')
      const data = await res.json()

      setServers(data.servers || [])
      setSources(data.sources || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (isOpen) {
      setSearchQuery('')
      setSelectedSource('all')
      setSelectedServer(null)
      setEnvOverrides({})
      void loadServers()
    }
  }, [isOpen, loadServers])

  useEffect(() => {
    if (!isOpen) return
    if (searchDebounceRef.current) {
      clearTimeout(searchDebounceRef.current)
    }
    const timeout = setTimeout(() => {
      void loadServers(searchQuery, selectedSource)
    }, 300)
    searchDebounceRef.current = timeout
    return () => clearTimeout(timeout)
  }, [searchQuery, selectedSource, loadServers, isOpen])

  const handleInstall = async (server: MCPServer) => {
    setInstalling(server.mcpId)
    setInstallSuccess(null)
    try {
      const envToSend = omitEmptySensitiveEnv(envOverrides)
      await installMCPServer(server.mcpId, envToSend, teamSlug, scope)
      setInstallSuccess(server.mcpId)
      setEnvOverrides({})
      if (onInstall) onInstall(server)
      setTimeout(() => setInstallSuccess(null), 3000)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setInstalling(null)
    }
  }

  return (
    <Dialog open={isOpen} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className="flex h-[85vh] w-full max-w-5xl flex-col gap-0 overflow-hidden border-panel-border bg-panel-background p-0 shadow-[var(--shadow-elevated)] sm:max-w-5xl"
        showCloseButton
      >
        <DialogHeader className="border-b px-4 py-4 text-left">
          <div className="flex items-center gap-3">
            <Package className="size-5 text-primary" />
            <div>
              <DialogTitle>MCP Store</DialogTitle>
              <DialogDescription>
                Browse and install MCP servers for this scope.
              </DialogDescription>
            </div>
            <Badge variant="secondary">{servers.length} servers</Badge>
          </div>
        </DialogHeader>

        <div className="border-b p-4">
          <div className="flex flex-col gap-3 sm:flex-row">
            <div className="relative flex-1">
              <Search className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                type="text"
                value={searchQuery}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSearchQuery(e.target.value)}
                placeholder="Search MCP servers..."
                className="bg-background pl-10"
              />
            </div>
            <Select value={selectedSource} onValueChange={setSelectedSource}>
              <SelectTrigger className="w-full bg-background sm:w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All Sources</SelectItem>
                {sources.map(source => (
                  <SelectItem key={source} value={source}>{source}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {loading && (
            <div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-6 animate-spin text-primary" />
              Loading MCP store...
            </div>
          )}

          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          {!loading && !error && servers.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center text-muted-foreground">
              <Package className="mb-4 size-12 opacity-30" />
              <p>No MCP servers found</p>
              {searchQuery && <p className="mt-1 text-sm">Try a different search term</p>}
            </div>
          )}

          {!loading && !error && servers.length > 0 && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              {servers.map(server => (
                <div
                  key={server.mcpId}
                  className={cn(
                    'cursor-pointer rounded-lg border bg-background p-4 transition-all hover:border-primary/50',
                    selectedServer?.mcpId === server.mcpId && 'border-primary ring-1 ring-primary/30'
                  )}
                  onClick={() => {
                    if (selectedServer?.mcpId === server.mcpId) {
                      setSelectedServer(null)
                      setEnvOverrides({})
                    } else {
                      setSelectedServer(server)
                      if (server.config?.env) {
                        const defaults: Record<string, string> = {}
                        Object.entries(server.config.env).forEach(([key, defaultValue]) => {
                          defaults[key] = defaultValue || ''
                        })
                        setEnvOverrides(defaults)
                      } else {
                        setEnvOverrides({})
                      }
                    }
                  }}
                >
                  <div className="mb-2 flex items-start justify-between">
                    <div className="min-w-0 flex-1">
                      <h3 className="truncate font-semibold text-foreground">{server.name}</h3>
                      <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
                        <span>by {server.author}</span>
                        {server.githubStars > 0 && (
                          <span className="flex items-center gap-1">
                            <Star className="size-3 fill-amber-400 text-amber-400" />
                            {server.githubStars.toLocaleString()}
                          </span>
                        )}
                      </div>
                    </div>
                    {server.config?.command && (
                      <Badge variant="outline" className="ml-2 shrink-0 font-mono">
                        {server.config.command}
                      </Badge>
                    )}
                  </div>

                  <p className="mb-3 line-clamp-2 text-sm text-muted-foreground">
                    {server.description}
                  </p>

                  {server.tags && server.tags.length > 0 && (
                    <div className="mb-3 flex flex-wrap gap-1">
                      {server.tags.slice(0, 4).map(tag => (
                        <Badge key={tag} variant="secondary">{tag}</Badge>
                      ))}
                      {server.tags.length > 4 && (
                        <span className="text-xs text-muted-foreground">+{server.tags.length - 4}</span>
                      )}
                    </div>
                  )}

                  {selectedServer?.mcpId === server.mcpId && (
                    <div className="mt-4 space-y-4 border-t pt-4" onClick={(e) => e.stopPropagation()}>
                      {server.config && (
                        <div>
                          <h4 className="mb-2 text-xs font-medium text-muted-foreground">Configuration</h4>
                          <pre className="overflow-x-auto rounded-md border bg-card p-3 text-xs text-muted-foreground">
                            {JSON.stringify(server.config, null, 2)}
                          </pre>
                        </div>
                      )}

                      {server.config?.env && Object.keys(server.config.env).length > 0 && (
                        <div>
                          <h4 className="mb-2 text-xs font-medium text-muted-foreground">Environment Variables</h4>
                          <p className="mb-2 text-[11px] text-muted-foreground">
                            Bind secrets to the credential store. Values are saved as{' '}
                            <code className="font-mono text-[10px]">{'{{CREDENTIAL:name:field}}'}</code>.
                          </p>
                          <div className="space-y-3">
                            {Object.entries(server.config.env).map(([key, defaultValue]) => {
                              const sensitive = isSensitiveEnvKey(key)
                              const current = envOverrides[key] ?? (sensitive ? '' : (defaultValue || ''))
                              return (
                                <div key={key} className="space-y-1">
                                  <Label htmlFor={`mcp-env-${server.mcpId}-${key}`} className="text-xs">{key}</Label>
                                  {sensitive ? (
                                    <CredentialBindControl
                                      id={`mcp-env-${server.mcpId}-${key}`}
                                      value={current}
                                      envKey={key}
                                      onChange={(value) => setEnvOverrides({ ...envOverrides, [key]: value })}
                                    />
                                  ) : (
                                    <Input
                                      id={`mcp-env-${server.mcpId}-${key}`}
                                      type="text"
                                      value={current}
                                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEnvOverrides({ ...envOverrides, [key]: e.target.value })}
                                      placeholder={defaultValue || 'Enter value...'}
                                      className="bg-card font-mono text-xs"
                                    />
                                  )}
                                </div>
                              )
                            })}
                          </div>
                        </div>
                      )}

                      <div className="flex items-center gap-2">
                        <Button
                          onClick={() => handleInstall(server)}
                          disabled={installing === server.mcpId}
                          size="sm"
                        >
                          {installing === server.mcpId ? (
                            <Loader2 className="animate-spin" />
                          ) : installSuccess === server.mcpId ? (
                            <Check />
                          ) : (
                            <Download />
                          )}
                          {installing === server.mcpId
                            ? 'Installing...'
                            : installSuccess === server.mcpId
                              ? 'Installed!'
                              : 'Install'}
                        </Button>

                        <Button variant="outline" size="sm" asChild>
                          <a
                            href={server.githubUrl}
                            target="_blank"
                            rel="noopener noreferrer"
                          >
                            <ExternalLink />
                            Docs
                          </a>
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
