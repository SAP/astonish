import { useEffect, useId, useRef, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import CredentialBindControl from '@/components/credentials/CredentialBindControl'
import { isSensitiveEnvKey, parseCredentialPlaceholder } from '@/components/credentials/credentialPlaceholders'

export type McpEnvMap = Record<string, string>

type McpEnvEditorProps = {
  env: McpEnvMap | undefined
  onChange: (env: McpEnvMap) => void
}

type EnvRow = { id: string; key: string; value: string }

function envToRows(env: McpEnvMap | undefined, idPrefix: string): EnvRow[] {
  if (!env || Object.keys(env).length === 0) return []
  return Object.entries(env).map(([key, value], i) => ({
    id: `${idPrefix}-${i}-${key}`,
    key,
    value: value ?? '',
  }))
}

function rowsToEnv(rows: EnvRow[]): McpEnvMap {
  const out: McpEnvMap = {}
  for (const row of rows) {
    const k = row.key.trim()
    if (!k) continue
    out[k] = row.value
  }
  return out
}

function envEqual(a: McpEnvMap, b: McpEnvMap): boolean {
  const aKeys = Object.keys(a)
  const bKeys = Object.keys(b)
  if (aKeys.length !== bKeys.length) return false
  return aKeys.every((k) => a[k] === b[k])
}

/**
 * Structured env editor for MCP servers.
 * Sensitive keys use credential bind; Source view still serializes {{CREDENTIAL:...}}.
 *
 * Rows are held in local state so empty draft keys (new variables) remain visible
 * until the user names them. Parent env only receives rows with non-empty keys.
 */
export default function McpEnvEditor({ env, onChange }: McpEnvEditorProps) {
  const idPrefix = useId()
  const seq = useRef(0)
  const [rows, setRows] = useState<EnvRow[]>(() => envToRows(env, idPrefix))

  // Sync from parent when external env changes (e.g. load/source→editor), but
  // do not wipe in-progress empty draft rows unless parent env actually differs
  // from what our non-empty rows would produce.
  useEffect(() => {
    const parent = env || {}
    const fromRows = rowsToEnv(rows)
    if (envEqual(parent, fromRows)) return
    setRows(envToRows(env, idPrefix))
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only re-sync when parent env identity/content changes
  }, [env])

  const commit = (next: EnvRow[]) => {
    setRows(next)
    onChange(rowsToEnv(next))
  }

  const setRow = (index: number, patch: Partial<Pick<EnvRow, 'key' | 'value'>>) => {
    commit(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)))
  }

  const removeRow = (index: number) => {
    commit(rows.filter((_, i) => i !== index))
  }

  const addRow = () => {
    seq.current += 1
    // Keep empty draft rows in local state only; parent map omits blank keys.
    setRows((prev) => [...prev, { id: `${idPrefix}-new-${seq.current}`, key: '', value: '' }])
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <Label className="text-sm text-muted-foreground">Environment</Label>
        <Button type="button" variant="outline" size="sm" className="h-7 text-xs gap-1" onClick={addRow}>
          <Plus size={12} />
          Add variable
        </Button>
      </div>

      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center">
          No environment variables. Add keys, and bind secrets to the credential store.
        </p>
      ) : (
        <div className="space-y-3">
          {rows.map((row, index) => {
            const bound = parseCredentialPlaceholder(row.value)
            return (
              <div
                key={row.id}
                className="rounded-lg border p-2.5 space-y-2 bg-muted/10"
              >
                <div className="flex items-start gap-2">
                  <div className="flex-1 space-y-1 min-w-0">
                    <Label className="text-[11px] uppercase tracking-wide text-muted-foreground">Variable</Label>
                    <Input
                      value={row.key}
                      onChange={(e) => setRow(index, { key: e.target.value })}
                      placeholder="e.g. GITHUB_TOKEN"
                      className="font-mono text-sm h-8"
                      autoFocus={row.key === '' && row.value === ''}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 mt-5 shrink-0 text-muted-foreground hover:text-destructive"
                    onClick={() => removeRow(index)}
                    aria-label="Remove variable"
                  >
                    <Trash2 size={14} />
                  </Button>
                </div>
                <div className="space-y-1">
                  <Label className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    Value{bound ? ' (credential)' : isSensitiveEnvKey(row.key) ? ' (prefer credential)' : ''}
                  </Label>
                  <CredentialBindControl
                    value={row.value}
                    envKey={row.key}
                    onChange={(value) => setRow(index, { value })}
                  />
                </div>
              </div>
            )
          })}
        </div>
      )}

      <p className="text-[11px] text-muted-foreground">
        Secrets should use credentials. Source view stores{' '}
        <code className="font-mono text-[10px]">{'{{CREDENTIAL:name:field}}'}</code> — never the raw secret.
      </p>
    </div>
  )
}

/** Compact badges for collapsed server cards. */
export function McpEnvBadges({ env }: { env?: McpEnvMap }) {
  if (!env || Object.keys(env).length === 0) return null
  const entries = Object.entries(env)
  return (
    <div className="flex flex-wrap gap-1 mt-1">
      {entries.slice(0, 3).map(([key, value]) => {
        const ref = parseCredentialPlaceholder(value)
        return (
          <span
            key={key}
            className="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-mono bg-muted/40 text-muted-foreground border border-border/60"
            title={ref ? `${key} → ${ref.name}:${ref.field}` : key}
          >
            {key}
            {ref ? ` → ${ref.name}` : ''}
          </span>
        )
      })}
      {entries.length > 3 && (
        <span className="text-[10px] text-muted-foreground">+{entries.length - 3} more</span>
      )}
    </div>
  )
}
