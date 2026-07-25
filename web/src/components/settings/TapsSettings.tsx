import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Plus, RefreshCw, Trash2 } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { fetchTaps, addTap, removeTap } from './settingsApi'
import type { TapEntry } from './settingsApi'
import { teamFetch } from '../../api/teamContext'

export default function TapsSettings({ teamSlug }: { teamSlug?: string }) {
  const [taps, setTaps] = useState<TapEntry[]>([])
  const [tapsLoading, setTapsLoading] = useState(false)
  const [tapsSuccess, setTapsSuccess] = useState<string | null>(null)
  const [newTapUrl, setNewTapUrl] = useState('')
  const [newTapAlias, setNewTapAlias] = useState('')
  const [tapsError, setTapsError] = useState<string | null>(null)

  const loadTaps = async () => {
    const data = await fetchTaps(teamSlug)
    setTaps(data.taps || [])
  }

  useEffect(() => {
    setTapsLoading(true)
    fetchTaps(teamSlug)
      .then(data => setTaps(data.taps || []))
      .catch((err: any) => setTapsError(err.message))
      .finally(() => setTapsLoading(false))
  }, [teamSlug])

  const handleAddTap = async () => {
    if (!newTapUrl) return
    setTapsError(null)
    setTapsLoading(true)
    try {
      await addTap(newTapUrl, newTapAlias, teamSlug)
      setNewTapUrl('')
      setNewTapAlias('')
      await loadTaps()
    } catch (err: any) {
      setTapsError(err.message)
    } finally {
      setTapsLoading(false)
    }
  }

  const handleRefreshTaps = async () => {
    setTapsLoading(true)
    setTapsError(null)
    setTapsSuccess(null)
    try {
      await teamFetch('/api/taps/update', { method: 'POST' }, teamSlug)
      await loadTaps()
      setTapsSuccess('Updated!')
      setTimeout(() => setTapsSuccess(null), 3000)
    } catch (err: any) {
      setTapsError(err.message)
    } finally {
      setTapsLoading(false)
    }
  }

  const handleRemoveTap = async (name: string) => {
    setTapsError(null)
    try {
      await removeTap(name, teamSlug)
      await loadTaps()
    } catch (err: any) {
      setTapsError(err.message)
    }
  }

  return (
    <div className="max-w-3xl space-y-6">
      <div>
        <h3 className="text-lg font-semibold text-foreground">Extension Repositories</h3>
        <p className="mt-1 text-sm text-muted-foreground">
          Manage extension repositories, or taps, that provide flows and MCP servers.
        </p>
      </div>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Add Repository</CardTitle>
          <CardDescription>
            Add a GitHub owner/repo shorthand or a full repository URL.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="tap-url">Repository URL or owner/repo</Label>
            <Input
              id="tap-url"
              type="text"
              value={newTapUrl}
              onChange={(e) => setNewTapUrl(e.target.value)}
              placeholder="SAP/astonish-flows"
              className="bg-background"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="tap-alias">Alias (optional)</Label>
            <Input
              id="tap-alias"
              type="text"
              value={newTapAlias}
              onChange={(e) => setNewTapAlias(e.target.value)}
              placeholder="my-flows"
              className="bg-background"
            />
          </div>
          {tapsError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertDescription>{tapsError}</AlertDescription>
            </Alert>
          )}
          <Button onClick={handleAddTap} disabled={tapsLoading || !newTapUrl}>
            {tapsLoading ? <Loader2 className="animate-spin" /> : <Plus />}
            Add Repository
          </Button>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader className="gap-3 sm:flex sm:flex-row sm:items-start sm:justify-between">
          <div className="space-y-1.5">
            <CardTitle className="text-base">Configured Repositories</CardTitle>
            <CardDescription>
              Refresh manifests from remote or remove custom repositories.
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            {tapsSuccess && (
              <span className="flex items-center gap-1 text-sm text-[color:var(--success)]">
                <Check className="size-4" />
                {tapsSuccess}
              </span>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleRefreshTaps}
              disabled={tapsLoading}
              title="Refresh manifests from remote"
            >
              <RefreshCw className={tapsLoading ? 'animate-spin' : ''} />
              {tapsLoading ? 'Refreshing...' : 'Refresh'}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {taps.length === 0 ? (
            <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
              No repositories configured. Add one above or click refresh.
            </div>
          ) : (
            <div className="space-y-3">
              {taps.map((tap) => (
                <div
                  key={tap.name}
                  className="flex items-center justify-between gap-4 rounded-lg border bg-background p-3"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-foreground">{tap.name}</span>
                      {tap.name === 'official' && <Badge variant="secondary">official</Badge>}
                    </div>
                    <div className="truncate text-sm text-muted-foreground">{tap.url}</div>
                  </div>
                  {tap.name !== 'official' && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      onClick={() => handleRemoveTap(tap.name)}
                      className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                      aria-label={`Remove ${tap.name}`}
                    >
                      <Trash2 />
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
