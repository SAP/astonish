import { useState, useEffect, useCallback } from 'react'
import { Loader2, Plus, Shield, ShieldOff, Trash2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import { teamFetch } from '../../api/teamContext'

interface NetworkPolicyRule {
  id: string
  host: string
  port: number
  action: string
  scope?: string
}

interface NetworkPolicySettingsProps {
  scope: string
  teamSlug?: string
  readOnly?: boolean
  rules?: NetworkPolicyRule[]
  onRulesChange?: () => void
}

/**
 * NetworkPolicySettings renders the network policy rules for a single scope.
 * It handles CRUD operations against /api/network-policies?scope=<scope>.
 */
export default function NetworkPolicySettings({ scope, teamSlug, readOnly = false, rules: externalRules, onRulesChange }: NetworkPolicySettingsProps) {
  const [rules, setRules] = useState<NetworkPolicyRule[]>([])
  const [loading, setLoading] = useState(false)
  const [showAddForm, setShowAddForm] = useState(false)
  const [newHost, setNewHost] = useState('')
  const [newPort, setNewPort] = useState('')
  const [newAction, setNewAction] = useState('allow')

  const fetchRules = useCallback(async () => {
    if (externalRules) {
      setRules(externalRules)
      return
    }
    setLoading(true)
    try {
      const url = `/api/network-policies?scope=${scope}`
      const res = await teamFetch(url, undefined, scope === 'platform' ? undefined : teamSlug)
      if (res.ok) {
        const data = await res.json()
        setRules(data.rules || [])
      }
    } catch (err) {
      console.error('Failed to fetch network policies:', err)
    } finally {
      setLoading(false)
    }
  }, [scope, teamSlug, externalRules])

  useEffect(() => {
    void fetchRules()
  }, [fetchRules])

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newHost.trim()) return

    const port = newPort ? parseInt(newPort, 10) : 0
    try {
      const url = `/api/network-policies?scope=${scope}`
      const res = await teamFetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host: newHost.trim(), port, action: newAction }),
      }, scope === 'platform' ? undefined : teamSlug)

      if (res.ok) {
        setNewHost('')
        setNewPort('')
        setNewAction('allow')
        setShowAddForm(false)
        void fetchRules()
        onRulesChange?.()
      }
    } catch (err) {
      console.error('Failed to create network policy:', err)
    }
  }

  const handleDelete = async (id: string) => {
    try {
      const url = `/api/network-policies/${id}?scope=${scope}`
      const res = await teamFetch(url, { method: 'DELETE' }, scope === 'platform' ? undefined : teamSlug)
      if (res.ok) {
        void fetchRules()
        onRulesChange?.()
      }
    } catch (err) {
      console.error('Failed to delete network policy:', err)
    }
  }

  if (loading && !externalRules) {
    return (
      <div className="flex items-center gap-2 rounded-lg border bg-card p-4 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin text-primary" />
        Loading network policies...
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {rules.length === 0 && !showAddForm && (
        <div className="rounded-lg border border-dashed bg-card/50 py-6 text-center text-sm text-muted-foreground">
          No network policy rules configured.
        </div>
      )}

      {rules.map((rule) => (
        <Card key={rule.id} className="flex-row items-center gap-3 border-border bg-card p-3 shadow-none">
          {rule.action === 'allow' ? (
            <Shield className="size-4 shrink-0 text-[color:var(--success)]" />
          ) : (
            <ShieldOff className="size-4 shrink-0 text-destructive" />
          )}

          <div className="min-w-0 flex-1">
            <div className="truncate font-mono text-sm text-foreground">
              {rule.host}{rule.port > 0 ? `:${rule.port}` : ''}
            </div>
          </div>

          <Badge variant={rule.action === 'allow' ? 'secondary' : 'destructive'}>
            {rule.action}
          </Badge>

          {rule.scope && rule.scope !== scope && (
            <Badge variant="outline" className="capitalize">
              {rule.scope}
            </Badge>
          )}

          {!readOnly && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => handleDelete(rule.id)}
              className="text-destructive hover:bg-destructive/10 hover:text-destructive"
              aria-label={`Delete ${rule.host}`}
              title="Delete rule"
            >
              <Trash2 />
            </Button>
          )}
        </Card>
      ))}

      {!readOnly && showAddForm && (
        <Card className="border-border bg-card p-3 shadow-none">
          <form onSubmit={handleAdd} className="grid gap-3 sm:grid-cols-[1fr_7rem_8rem_auto_auto] sm:items-end">
            <div className="space-y-2">
              <Label htmlFor={`network-host-${scope}`}>Host pattern</Label>
              <Input
                id={`network-host-${scope}`}
                type="text"
                value={newHost}
                onChange={(e) => setNewHost(e.target.value)}
                placeholder="e.g., *.example.com"
                className="bg-background"
                autoFocus
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor={`network-port-${scope}`}>Port</Label>
              <Input
                id={`network-port-${scope}`}
                type="number"
                value={newPort}
                onChange={(e) => setNewPort(e.target.value)}
                placeholder="any"
                min="0"
                max="65535"
                className="bg-background"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor={`network-action-${scope}`}>Action</Label>
              <Select value={newAction} onValueChange={setNewAction}>
                <SelectTrigger id={`network-action-${scope}`} className="bg-background">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="allow">Allow</SelectItem>
                  <SelectItem value="deny">Deny</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button type="submit" disabled={!newHost.trim()}>
              Add
            </Button>
            <Button type="button" variant="secondary" onClick={() => setShowAddForm(false)}>
              Cancel
            </Button>
          </form>
        </Card>
      )}

      {!readOnly && !showAddForm && (
        <Button type="button" variant="ghost" size="sm" onClick={() => setShowAddForm(true)}>
          <Plus />
          Add rule
        </Button>
      )}
    </div>
  )
}
