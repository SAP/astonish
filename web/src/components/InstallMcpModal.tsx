import React, { useState, useEffect } from 'react'
import { Lock, Save, Loader2, AlertCircle } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import CredentialBindControl from '@/components/credentials/CredentialBindControl'
import { isSensitiveEnvKey, omitEmptySensitiveEnv } from '@/components/credentials/credentialPlaceholders'

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
      Object.keys(server.config.env).forEach(key => {
        const isSensitive = isSensitiveEnvKey(key)
        // Sensitive keys start empty so the user binds a credential or creates a secret.
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
      await onInstall(omitEmptySensitiveEnv(envVars))
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
                  <p className="text-xs text-muted-foreground">
                    Bind secrets to the credential store. Config will save{' '}
                    <code className="font-mono text-[10px]">{'{{CREDENTIAL:name:field}}'}</code>, not raw tokens.
                  </p>

                  <div className="space-y-4">
                    {Object.keys(server.config.env).map(key => {
                      const isSensitive = isSensitiveEnvKey(key)
                      return (
                        <div key={key} className="space-y-2">
                          <Label htmlFor={`env-${key}`} className="text-xs uppercase tracking-wider text-muted-foreground">
                            {key}
                          </Label>
                          {isSensitive ? (
                            <CredentialBindControl
                              id={`env-${key}`}
                              value={envVars[key] || ''}
                              envKey={key}
                              onChange={(value) => handleEnvChange(key, value)}
                            />
                          ) : (
                            <Input
                              id={`env-${key}`}
                              type="text"
                              value={envVars[key] || ''}
                              onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleEnvChange(key, e.target.value)}
                              placeholder={`Enter ${key}`}
                              className="font-mono text-sm"
                            />
                          )}
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
                  <AlertCircle size={18} />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose} disabled={isInstalling}>
                Cancel
              </Button>
              <Button type="submit" disabled={isInstalling}>
                {isInstalling ? (
                  <>
                    <Loader2 size={16} className="mr-2 animate-spin" />
                    Installing…
                  </>
                ) : (
                  <>
                    <Save size={16} className="mr-2" />
                    Install
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
