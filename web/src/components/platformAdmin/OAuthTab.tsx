import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Copy, KeyRound, Loader2, Plus, RefreshCw, ShieldCheck } from 'lucide-react'

import * as adminApi from '../../api/platformAdmin'
import type { OAuthClient, OAuthClientInput, OAuthDiscovery } from '../../api/platformAdmin'
import { InlineError, InlineSuccess } from './shared'
import { gradientAmber, inputStyle } from './sharedStyles'

const csv = (value: string) => value.split(/[\s,]+/).map(item => item.trim()).filter(Boolean)
const toCSV = (values: string[]) => values.join(' ')

export default function OAuthTab() {
  const [clients, setClients] = useState<OAuthClient[]>([])
  const [discovery, setDiscovery] = useState<OAuthDiscovery | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [editing, setEditing] = useState<OAuthClient | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [revealedSecret, setRevealedSecret] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [nextDiscovery, nextClients] = await Promise.all([adminApi.getOAuthDiscovery(), adminApi.listOAuthClients()])
      setDiscovery(nextDiscovery)
      setClients(nextClients)
    } catch (cause) {
      setError((cause as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    if (!success) return
    const timer = window.setTimeout(() => setSuccess(''), 4000)
    return () => window.clearTimeout(timer)
  }, [success])

  const copy = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value)
      setSuccess(`${label} copied`)
    } catch {
      setError(`Could not copy ${label.toLowerCase()}`)
    }
  }

  const saved = (message: string, secret: string) => {
    setShowCreate(false)
    setEditing(null)
    setRevealedSecret(secret)
    setSuccess(message)
    void load()
  }

  return <div className="flex-1 overflow-y-auto px-6 py-4 space-y-6">
    {(error || success) && <div>{error && <InlineError msg={error} />}{success && <InlineSuccess msg={success} />}</div>}

    <section className="rounded-xl p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-start justify-between gap-4"><div>
        <h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Astonish OAuth endpoints</h2>
        <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Use these issuer-owned endpoints with PKCE clients and the protected MCP resource.</p>
      </div><ShieldCheck size={20} style={{ color: 'var(--brand)' }} /></div>
      {loading ? <Loading /> : discovery && <dl className="mt-4 grid gap-2 text-xs">
        <Endpoint label="Issuer" value={discovery.issuer} onCopy={copy} />
        <Endpoint label="Authorization" value={discovery.authorization_endpoint} onCopy={copy} />
        <Endpoint label="Token" value={discovery.token_endpoint} onCopy={copy} />
        <Endpoint label="JWKS" value={discovery.jwks_uri} onCopy={copy} />
        <Endpoint label="MCP resource" value={discovery.resource} onCopy={copy} />
      </dl>}
    </section>

    {revealedSecret && <section className="rounded-xl p-4" style={{ background: 'var(--warning-soft)', border: '1px solid var(--warning)' }}>
      <h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Copy the new client secret now</h2>
      <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>It is shown once and is never stored in Studio.</p>
      <div className="mt-3 flex gap-2"><code className="min-w-0 flex-1 break-all rounded p-2 text-xs" style={{ background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>{revealedSecret}</code><button onClick={() => void copy(revealedSecret, 'Client secret')} className="p-2 rounded" style={{ color: 'var(--brand)' }} title="Copy client secret"><Copy size={16} /></button></div>
      <button onClick={() => setRevealedSecret('')} className="mt-3 text-xs font-medium" style={{ color: 'var(--brand)' }}>I copied it; hide secret</button>
    </section>}

    <section><div className="flex items-center justify-between mb-3"><div><h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>OAuth clients</h2><p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Only platform administrators can manage clients. Disabling a client stops new grants.</p></div><button onClick={() => setShowCreate(true)} className="flex items-center gap-2 px-3 py-2 rounded-xl text-sm font-medium text-white" style={gradientAmber}><Plus size={15} /> Add client</button></div>
      {loading ? <Loading /> : clients.length === 0 ? <div className="rounded-xl py-10 text-center" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}>No OAuth clients configured.</div> : <div className="space-y-3">{clients.map(client => <article key={client.client_id} className="rounded-xl p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}><div className="flex justify-between gap-4"><div className="min-w-0"><div className="flex items-center gap-2"><h3 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{client.name}</h3><span className="text-[10px] rounded-full px-2 py-0.5" style={{ background: client.active ? 'var(--success-soft)' : 'var(--bg-tertiary)', color: client.active ? 'var(--success)' : 'var(--text-muted)' }}>{client.active ? 'Active' : 'Disabled'}</span></div><code className="text-xs" style={{ color: 'var(--text-muted)' }}>{client.client_id}</code><p className="mt-2 text-xs" style={{ color: 'var(--text-secondary)' }}>{client.client_type} · {client.grant_types.join(', ')} · scopes: {client.scopes.join(', ') || 'none'}</p></div><button onClick={() => setEditing(client)} className="text-xs font-medium" style={{ color: 'var(--brand)' }}>Manage</button></div></article>)}</div>}
    </section>
    {(showCreate || editing) && <ClientModal client={editing} onCancel={() => { setShowCreate(false); setEditing(null) }} onError={setError} onSaved={saved} />}
  </div>
}

function Endpoint({ label, value, onCopy }: { label: string; value: string; onCopy: (value: string, label: string) => Promise<void> }) {
  return <div className="grid grid-cols-[7rem_1fr_auto] gap-2 items-center"><dt style={{ color: 'var(--text-muted)' }}>{label}</dt><dd className="font-mono break-all" style={{ color: 'var(--text-secondary)' }}>{value}</dd><button onClick={() => void onCopy(value, label)} style={{ color: 'var(--brand)' }} title={`Copy ${label}`}><Copy size={14} /></button></div>
}
function Loading() { return <div className="flex justify-center py-8"><Loader2 size={20} className="animate-spin" style={{ color: 'var(--text-muted)' }} /></div> }

function ClientModal({ client, onCancel, onError, onSaved }: { client: OAuthClient | null; onCancel: () => void; onError: (message: string) => void; onSaved: (message: string, secret: string) => void }) {
  const [name, setName] = useState(client?.name || '')
  const [type, setType] = useState<'public' | 'confidential'>(client?.client_type || 'public')
  const [redirects, setRedirects] = useState(toCSV(client?.redirect_uris || []))
  const [grants, setGrants] = useState(toCSV(client?.grant_types || ['authorization_code']))
  const [resources, setResources] = useState(toCSV(client?.resources || []))
  const [scopes, setScopes] = useState(toCSV(client?.scopes || ['tool:execute']))
  const [active, setActive] = useState(client?.active ?? true)
  const [rotate, setRotate] = useState(false)
  const [saving, setSaving] = useState(false)
  const submit = async (event: FormEvent) => { event.preventDefault(); setSaving(true); try { const input: OAuthClientInput = { name, client_type: type, redirect_uris: csv(redirects), grant_types: csv(grants), resources: csv(resources), scopes: csv(scopes), active, rotate_secret: rotate }; const result = client ? await adminApi.updateOAuthClient(client.client_id, input) : await adminApi.createOAuthClient(input); onSaved(`OAuth client ${client ? 'updated' : 'created'}`, result.client_secret) } catch (cause) { onError((cause as Error).message) } finally { setSaving(false) } }
  return <div className="fixed inset-0 z-50 flex items-center justify-center"><div className="absolute inset-0 bg-black/60" onClick={onCancel} /><form onSubmit={submit} className="relative max-h-[90vh] w-full max-w-xl overflow-y-auto rounded-2xl p-6 space-y-4" style={{ background: 'var(--bg-secondary)' }}><div className="flex justify-between"><div><h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>{client ? `Manage ${client.name}` : 'Add OAuth client'}</h2><p className="text-xs" style={{ color: 'var(--text-muted)' }}>Public clients require authorization-code PKCE; confidential clients receive a secret.</p></div><KeyRound style={{ color: 'var(--brand)' }} /></div><Field label="Name"><input required value={name} onChange={e => setName(e.target.value)} style={inputStyle} className="w-full p-2 rounded" /></Field><Field label="Client type"><select value={type} onChange={e => setType(e.target.value as 'public' | 'confidential')} style={inputStyle} className="w-full p-2 rounded"><option value="public">Public (PKCE)</option><option value="confidential">Confidential</option></select></Field><Field label="Redirect URIs"><input value={redirects} onChange={e => setRedirects(e.target.value)} placeholder="https://app.example/callback" style={inputStyle} className="w-full p-2 rounded font-mono" /></Field><Field label="Grant types"><input required value={grants} onChange={e => setGrants(e.target.value)} placeholder="authorization_code refresh_token" style={inputStyle} className="w-full p-2 rounded" /></Field><Field label="Allowed resources"><input required value={resources} onChange={e => setResources(e.target.value)} placeholder="https://astonish.example/api/mcp" style={inputStyle} className="w-full p-2 rounded font-mono" /></Field><Field label="Allowed scopes"><input value={scopes} onChange={e => setScopes(e.target.value)} placeholder="tool:execute offline_access" style={inputStyle} className="w-full p-2 rounded" /></Field><label className="flex gap-2 text-sm" style={{ color: 'var(--text-secondary)' }}><input type="checkbox" checked={active} onChange={e => setActive(e.target.checked)} /> Allow new grants</label>{client?.client_type === 'confidential' && <label className="flex gap-2 text-sm" style={{ color: 'var(--text-secondary)' }}><input type="checkbox" checked={rotate} onChange={e => setRotate(e.target.checked)} /> Rotate the client secret (the old secret stops working)</label>}<div className="flex gap-3 pt-2"><button type="button" onClick={onCancel} className="flex-1 p-2 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button><button disabled={saving} className="flex flex-1 justify-center gap-2 rounded p-2 text-white disabled:opacity-50" style={gradientAmber}>{saving ? <Loader2 size={16} className="animate-spin" /> : <RefreshCw size={16} />}{client ? 'Save' : 'Create'}</button></div></form></div>
}
function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="block text-sm"><span className="mb-1 block" style={{ color: 'var(--text-secondary)' }}>{label}</span>{children}</label> }
