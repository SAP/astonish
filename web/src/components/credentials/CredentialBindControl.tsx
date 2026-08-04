import { useCallback, useEffect, useMemo, useState } from 'react'
import { Key, Loader2, Plus, Search } from 'lucide-react'
import { teamFetch } from '@/api/teamContext'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import CredentialFormDialog from './CredentialFormDialog'
import {
  defaultFieldForType,
  fieldsForType,
  formatCredentialPlaceholder,
  guessCredentialTypeFromEnvKey,
  parseCredentialPlaceholder,
  type CredentialRef,
} from './credentialPlaceholders'

export type CredentialSummary = {
  name: string
  type: string
  scope?: string
  shadowed?: boolean
}

type CredentialBindControlProps = {
  /** Current env/config value (plain string or {{CREDENTIAL:...}}). */
  value: string
  onChange: (value: string) => void
  /** Env var name for heuristics (type default, create name suggestion). */
  envKey?: string
  /** Optional className for the outer wrapper. */
  className?: string
  /** When true, hide the plain-text input path (credential-only). */
  credentialOnly?: boolean
}

async function fetchCredentialList(): Promise<CredentialSummary[]> {
  const res = await teamFetch('/api/credentials')
  if (!res.ok) throw new Error('Failed to load credentials')
  const data = await res.json()
  const list = (data.credentials || []) as CredentialSummary[]
  return list
}

/**
 * Control for binding a value to a credential store entry, or entering plain text.
 * When bound, the stored value is always {{CREDENTIAL:name:field}}.
 * "New credential" reuses the full Credentials add dialog (all types, username+password, etc.).
 */
export default function CredentialBindControl({
  value,
  onChange,
  envKey = '',
  className,
  credentialOnly = false,
}: CredentialBindControlProps) {
  const ref = parseCredentialPlaceholder(value)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)

  const createInitial = useMemo(() => {
    const suggested = envKey
      ? envKey.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
      : ''
    return {
      name: suggested,
      type: guessCredentialTypeFromEnvKey(envKey),
    }
  }, [envKey])

  return (
    <div className={className}>
      {ref ? (
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="secondary" className="font-mono text-xs gap-1 max-w-full">
            <Key size={11} className="shrink-0 text-primary" />
            <span className="truncate">{ref.name}</span>
            <span className="text-muted-foreground">·</span>
            <span className="text-muted-foreground">{ref.field}</span>
          </Badge>
          <Button type="button" variant="outline" size="sm" className="h-7 text-xs" onClick={() => setPickerOpen(true)}>
            Change
          </Button>
          {!credentialOnly && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 text-xs text-muted-foreground"
              onClick={() => onChange('')}
            >
              Clear
            </Button>
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          {!credentialOnly && (
            <Input
              value={value}
              onChange={(e) => onChange(e.target.value)}
              placeholder={envKey ? `Value for ${envKey}` : 'Plain value'}
              className="font-mono text-sm h-8 flex-1"
            />
          )}
          <div className="flex items-center gap-1.5 shrink-0">
            <Button type="button" variant="outline" size="sm" className="h-8 text-xs gap-1" onClick={() => setPickerOpen(true)}>
              <Key size={12} />
              Credential
            </Button>
            <Button type="button" variant="secondary" size="sm" className="h-8 text-xs gap-1" onClick={() => setCreateOpen(true)}>
              <Plus size={12} />
              New credential
            </Button>
          </div>
        </div>
      )}

      <CredentialPickerDialog
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        envKey={envKey}
        current={ref}
        onSelect={(next) => {
          onChange(formatCredentialPlaceholder(next.name, next.field))
          setPickerOpen(false)
        }}
        onCreateNew={() => {
          setPickerOpen(false)
          setCreateOpen(true)
        }}
      />

      <CredentialFormDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        initialForm={createInitial}
        title="Add Credential"
        description={
          envKey
            ? `Creates a store credential and binds ${envKey} as {{CREDENTIAL:name:field}}. The secret is never written into MCP config.`
            : 'Creates a store credential and binds it. The secret is never written into MCP config.'
        }
        saveLabel="Create & bind"
        onSaved={({ name, type }) => {
          onChange(formatCredentialPlaceholder(name, defaultFieldForType(type)))
        }}
      />
    </div>
  )
}

function CredentialPickerDialog({
  open,
  onOpenChange,
  envKey,
  current,
  onSelect,
  onCreateNew,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  envKey: string
  current: CredentialRef | null
  onSelect: (ref: CredentialRef) => void
  onCreateNew: () => void
}) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [creds, setCreds] = useState<CredentialSummary[]>([])
  const [query, setQuery] = useState('')
  const [selectedName, setSelectedName] = useState(current?.name || '')
  const [selectedField, setSelectedField] = useState(current?.field || '')

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await fetchCredentialList()
      setCreds(list)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load credentials')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (open) {
      setQuery('')
      setSelectedName(current?.name || '')
      setSelectedField(current?.field || '')
      void load()
    }
  }, [open, current, load])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return creds
    return creds.filter(
      (c) => c.name.toLowerCase().includes(q) || (c.type || '').toLowerCase().includes(q),
    )
  }, [creds, query])

  const selectedCred = creds.find((c) => c.name === selectedName)
  const fieldOptions = selectedCred ? fieldsForType(selectedCred.type) : []

  useEffect(() => {
    if (!selectedCred) return
    if (!selectedField || !fieldOptions.includes(selectedField)) {
      setSelectedField(defaultFieldForType(selectedCred.type))
    }
  }, [selectedCred, selectedField, fieldOptions])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Select credential</DialogTitle>
          <DialogDescription>
            {envKey
              ? `Bind ${envKey} to a credential from the store. The config will store {{CREDENTIAL:name:field}}.`
              : 'Bind this value to a credential from the store.'}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 py-2">
          <div className="relative">
            <Search size={14} className="absolute left-2.5 top-2.5 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search credentials…"
              className="pl-8 h-9 text-sm"
              autoFocus
            />
          </div>

          {loading ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground text-sm gap-2">
              <Loader2 size={16} className="animate-spin" />
              Loading…
            </div>
          ) : error ? (
            <p className="text-sm text-destructive">{error}</p>
          ) : filtered.length === 0 ? (
            <div className="text-center py-6 space-y-2">
              <p className="text-sm text-muted-foreground">No credentials found.</p>
              <Button type="button" size="sm" variant="secondary" onClick={onCreateNew}>
                <Plus size={14} className="mr-1" />
                Add credential
              </Button>
            </div>
          ) : (
            <ul className="max-h-48 overflow-y-auto rounded-md border divide-y">
              {filtered.map((c) => (
                <li key={`${c.scope || ''}:${c.name}`}>
                  <button
                    type="button"
                    className={`w-full text-left px-3 py-2 text-sm hover:bg-muted/60 transition-colors flex items-center justify-between gap-2 ${
                      selectedName === c.name ? 'bg-primary/10' : ''
                    }`}
                    onClick={() => setSelectedName(c.name)}
                  >
                    <span className="font-mono truncate">{c.name}</span>
                    <span className="flex items-center gap-1 shrink-0">
                      {c.scope ? (
                        <Badge variant="outline" className="text-[10px] h-5 px-1.5">
                          {c.scope}
                        </Badge>
                      ) : null}
                      <Badge variant="secondary" className="text-[10px] h-5 px-1.5">
                        {c.type}
                      </Badge>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}

          {selectedCred && (
            <div className="space-y-1.5">
              <Label className="text-xs text-muted-foreground">Field</Label>
              <select
                className="w-full h-9 rounded-md border bg-background px-2 text-sm font-mono"
                value={selectedField}
                onChange={(e) => setSelectedField(e.target.value)}
              >
                {fieldOptions.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </select>
              <p className="text-[11px] text-muted-foreground font-mono">
                {formatCredentialPlaceholder(selectedCred.name, selectedField || defaultFieldForType(selectedCred.type))}
              </p>
            </div>
          )}
        </div>

        <DialogFooter className="gap-2 sm:gap-0">
          <Button type="button" variant="ghost" size="sm" onClick={onCreateNew}>
            <Plus size={14} className="mr-1" />
            Add credential
          </Button>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={!selectedName || !selectedField}
            onClick={() => onSelect({ name: selectedName, field: selectedField })}
          >
            Bind
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
