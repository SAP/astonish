import React, { useState, useEffect } from 'react'
import { Lock, Save, Loader2, AlertCircle } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

interface McpServerConfig {
  env?: Record<string, string>
}

interface McpServer {
  name: string
  description: string
  config?: McpServerConfig
}

interface InstallMcpModalProps {
  isOpen: boolean
  onClose: () => void
  onInstall: (envVars: Record<string, string>) => Promise<void>
  server: McpServer | null
}

export default function InstallMcpModal({ isOpen, onClose, onInstall, server }: InstallMcpModalProps) {
  const [envVars, setEnvVars] = useState<Record<string, string>>({})
  const [isInstalling, setIsInstalling] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Initialize form with required env vars.
  useEffect(() => {
    if (server?.config?.env) {
      const initial: Record<string, string> = {}
      // The store provides env as a map of key -> description/value.
      // Capture values for keys, but skip defaults for sensitive fields.
      Object.keys(server.config.env).forEach(key => {
        const isSensitive = /TOKEN|KEY|SECRET|PASSWORD|PASSWD|PWD|AUTH/i.test(key)
        initial[key] = isSensitive ? '' : (server.config!.env![key] || '')
      })
      setEnvVars(initial)
    } else {
      setEnvVars({})
    }
    setError(null)
  }, [server])

  const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!server) return
    setIsInstalling(true)
    setError(null)

    try {
      await onInstall(envVars)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to install server')
    } finally {
      setIsInstalling(false)
    }
  }

  const handleEnvChange = (key: string, value: string) => {
    setEnvVars(prev => ({
      ...prev,
      [key]: value,
    }))
  }

  return (
    <Dialog open={isOpen && Boolean(server)} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-lg border-panel-border bg-panel-background shadow-[var(--shadow-elevated)]">
        {server && (
          <form onSubmit={handleSubmit}>
            <DialogHeader>
              <DialogTitle>Install {server.name}</DialogTitle>
              <DialogDescription>{server.description}</DialogDescription>
            </DialogHeader>

            <div className="space-y-6 py-6">
              {server.config?.env && Object.keys(server.config.env).length > 0 ? (
                <div className="space-y-4">
                  <div className="flex items-center gap-2 text-sm font-medium text-foreground">
                    <Lock size={16} className="text-primary" />
                    Configuration Required
                  </div>

                  <div className="space-y-4">
                    {Object.keys(server.config.env).map(key => {
                      const isSensitive = /TOKEN|KEY|SECRET|PASSWORD|PASSWD|PWD|AUTH/i.test(key)
                      return (
                        <div key={key} className="space-y-2">
                          <Label htmlFor={`env-${key}`} className="text-xs uppercase tracking-wider text-muted-foreground">
                            {key}
                          </Label>
                          <Input
                            id={`env-${key}`}
                            type={isSensitive ? 'password' : 'text'}
                            value={envVars[key] || ''}
                            onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleEnvChange(key, e.target.value)}
                            placeholder={`Enter ${key}`}
                            className="font-mono text-sm"
                          />
                        </div>
                      )
                    })}
                  </div>
                </div>
              ) : (
                <Alert>
                  <AlertCircle size={18} />
                  <AlertDescription>
                    This server does not require any additional configuration. Click Install to proceed.
                  </AlertDescription>
                </Alert>
              )}

              {error && (
                <Alert variant="destructive">
                  <AlertCircle size={16} />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </div>

            <DialogFooter>
              <Button type="button" variant="secondary" onClick={onClose} disabled={isInstalling}>
                Cancel
              </Button>
              <Button type="submit" disabled={isInstalling}>
                {isInstalling ? (
                  <>
                    <Loader2 size={16} className="animate-spin" />
                    Installing...
                  </>
                ) : (
                  <>
                    <Save size={16} />
                    Install Server
                  </>
                )}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
