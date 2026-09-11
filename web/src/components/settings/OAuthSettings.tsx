import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Copy, KeyRound, Loader2, Plus, RefreshCw, ShieldCheck } from 'lucide-react'

import * as oauthApi from '../../api/oauth'
import type { OAuthClient, OAuthClientInput, OAuthContext, OAuthDiscovery } from '../../api/oauth'
import OAuthScopeSelector from '../oauth/OAuthScopeSelector'

const csv = (value: string) => value.split(/[\s,]+/).map(item => item.trim()).filter(Boolean)
const toCSV = (values: string[]) => values.join(' ')
const inputStyle = { background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }

export default function OAuthSettings() {
  const [clients, setClients] = useState<OAuthClient[]>([])
  const [contexts, setContexts] = useState<OAuthContext[]>([])
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
      const [nextDiscovery, nextClients, nextContexts] = await Promise.all([
        oauthApi.getOAuthDiscovery(), oauthApi.listOAuthClients(), oauthApi.listOAuthContexts(),
      ])
      setDiscovery(nextDiscovery)
      setClients(nextClients)
      setContexts(nextContexts)
    } catch (cause) { setError((cause as Error).message) } finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    if (!success) return
    const timer = window.setTimeout(() => setSuccess(''), 4000)
    return () => window.clearTimeout(timer)
  }, [success])

  const copy = async (value: string, label: string) => {
    try { await navigator.clipboard.writeText(value); setSuccess(`${label} copied`) } catch { setError(`Could not copy ${label.toLowerCase()}`) }
  }
  const saved = (message: string, secret: string) => {
    setShowCreate(false); setEditing(null); setRevealedSecret(secret); setSuccess(message); void load()
  }

  return <div className="flex-1 overflow-y-auto p-6 space-y-6">
    {(error || success) && <div className="space-y-2">{error && <Notice tone="error">{error}</Notice>}{success && <Notice tone="success">{success}</Notice>}</div>}
    <section className="rounded-xl p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-start justify-between gap-4"><div><h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Astonish OAuth endpoints</h2><p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Use these issuer-owned endpoints with PKCE clients and the protected MCP resource.</p></div><ShieldCheck size={20} style={{ color: 'var(--brand)' }} /></div>
      {loading ? <Loading /> : discovery && <dl className="mt-4 grid gap-2 text-xs"><Endpoint label="Issuer" value={discovery.issuer} onCopy={copy} /><Endpoint label="Authorization" value={discovery.authorization_endpoint} onCopy={copy} /><Endpoint label="Token" value={discovery.token_endpoint} onCopy={copy} /><Endpoint label="JWKS" value={discovery.jwks_uri} onCopy={copy} /><Endpoint label="MCP resource" value={discovery.resource} onCopy={copy} /></dl>}
    </section>
    {revealedSecret && <section className="rounded-xl p-4" style={{ background: 'var(--warning-soft)', border: '1px solid var(--warning)' }}><h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Copy the new client secret now</h2><p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>It is shown once and is never stored in Studio.</p><div className="mt-3 flex gap-2"><code className="min-w-0 flex-1 break-all rounded p-2 text-xs" style={{ background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>{revealedSecret}</code><button onClick={() => void copy(revealedSecret, 'Client secret')} className="p-2 rounded" style={{ color: 'var(--brand)' }} title="Copy client secret"><Copy size={16} /></button></div><button onClick={() => setRevealedSecret('')} className="mt-3 text-xs font-medium" style={{ color: 'var(--brand)' }}>I copied it; hide secret</button></section>}
    <section><div className="flex items-center justify-between mb-3"><div><h2 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Your OAuth clients</h2><p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Clients are private to your account. Each client is bound to one organization and team.</p></div><button onClick={() => setShowCreate(true)} disabled={!discovery} className="flex items-center gap-2 px-3 py-2 rounded-xl text-sm font-medium text-white disabled:opacity-50" style={{ background: 'var(--brand)' }}><Plus size={15} /> Add client</button></div>
      {loading ? <Loading /> : clients.length === 0 ? <div className="rounded-xl py-10 text-center" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}>No OAuth clients configured.</div> : <div className="space-y-3">{clients.map(client => <article key={client.client_id} className="rounded-xl p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}><div className="flex justify-between gap-4"><div className="min-w-0"><div className="flex items-center gap-2"><h3 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{client.name}</h3><span className="text-[10px] rounded-full px-2 py-0.5" style={{ background: client.active ? 'var(--success-soft)' : 'var(--bg-tertiary)', color: client.active ? 'var(--success)' : 'var(--text-muted)' }}>{client.active ? 'Active' : 'Disabled'}</span></div><code className="text-xs" style={{ color: 'var(--text-muted)' }}>{client.client_id}</code><p className="mt-2 text-xs" style={{ color: 'var(--text-secondary)' }}>{client.client_type} · {client.grant_types.join(', ')} · scopes: {client.scopes.join(', ') || 'none'}</p></div><button onClick={() => setEditing(client)} className="text-xs font-medium" style={{ color: 'var(--brand)' }}>Manage</button></div></article>)}</div>}
    </section>
    {(showCreate || editing) && discovery && <ClientModal client={editing} contexts={contexts} scopeOptions={discovery.scopes} onCancel={() => { setShowCreate(false); setEditing(null) }} onError={setError} onSaved={saved} />}
  </div>
}

function ClientModal({ client, contexts, scopeOptions, onCancel, onError, onSaved }: { client: OAuthClient | null; contexts: OAuthContext[]; scopeOptions: OAuthDiscovery['scopes']; onCancel: () => void; onError: (message: string) => void; onSaved: (message: string, secret: string) => void }) {
  const [name, setName] = useState(client?.name || '')
  const [type, setType] = useState<'public' | 'confidential'>(client?.client_type || 'public')
  const [redirects, setRedirects] = useState(toCSV(client?.redirect_uris || []))
  const [grants, setGrants] = useState(toCSV(client?.grant_types || ['authorization_code']))
  const [resources, setResources] = useState(toCSV(client?.resources || []))
  const [scopes, setScopes] = useState<string[]>(client?.scopes || scopeOptions.map(scope => scope.value))
  const [active, setActive] = useState(client?.active ?? true)
  const [orgID, setOrgID] = useState(client?.org_id || (contexts.length === 1 ? contexts[0].id : ''))
  const selectedContext = contexts.find(context => context.id === orgID)
  const [teamID, setTeamID] = useState(client?.team_id || (contexts.length === 1 && contexts[0].teams.length === 1 ? contexts[0].teams[0].id : ''))
  const [rotate, setRotate] = useState(false)
  const [saving, setSaving] = useState(false)
  const chooseOrg = (nextOrgID: string) => { setOrgID(nextOrgID); const context = contexts.find(item => item.id === nextOrgID); setTeamID(context?.teams.length === 1 ? context.teams[0].id : '') }
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (scopes.length === 0) { onError('Select at least one permission'); return }
    setSaving(true)
    try {
      const input: OAuthClientInput = { name, client_type: type, org_id: orgID, team_id: teamID, redirect_uris: csv(redirects), grant_types: csv(grants), resources: csv(resources), scopes, active, rotate_secret: rotate }
      const result = client ? await oauthApi.updateOAuthClient(client.client_id, input) : await oauthApi.createOAuthClient(input)
      onSaved(`OAuth client ${client ? 'updated' : 'created'}`, result.client_secret)
    } catch (cause) { onError((cause as Error).message) } finally { setSaving(false) }
  }
  const fixedContext = Boolean(client)
  return <div className="fixed inset-0 z-50 flex items-center justify-center"><div className="absolute inset-0 bg-black/60" onClick={onCancel} /><form onSubmit={submit} className="relative max-h-[90vh] w-full max-w-xl overflow-y-auto rounded-2xl p-6 space-y-4" style={{ background: 'var(--bg-secondary)' }}><div className="flex justify-between"><div><h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>{client ? `Manage ${client.name}` : 'Add OAuth client'}</h2><p className="text-xs" style={{ color: 'var(--text-muted)' }}>Public clients require authorization-code PKCE; confidential clients receive a secret.</p></div><KeyRound style={{ color: 'var(--brand)' }} /></div><Field label="Name"><input required value={name} onChange={e => setName(e.target.value)} style={inputStyle} className="w-full p-2 rounded" /></Field><Field label="Client type"><select value={type} onChange={e => setType(e.target.value as 'public' | 'confidential')} style={inputStyle} className="w-full p-2 rounded"><option value="public">Public (PKCE)</option><option value="confidential">Confidential</option></select></Field><Field label="Organization"><select required disabled={fixedContext} value={orgID} onChange={e => chooseOrg(e.target.value)} style={inputStyle} className="w-full p-2 rounded"><option value="">Select organization</option>{contexts.map(context => <option key={context.id} value={context.id}>{context.name}</option>)}</select></Field><Field label="Team"><select required disabled={fixedContext || !selectedContext} value={teamID} onChange={e => setTeamID(e.target.value)} style={inputStyle} className="w-full p-2 rounded"><option value="">Select team</option>{selectedContext?.teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}</select></Field>{fixedContext && <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Organization and team are immutable after creation.</p>}<Field label="Redirect URIs"><input value={redirects} onChange={e => setRedirects(e.target.value)} placeholder="https://app.example/callback" style={inputStyle} className="w-full p-2 rounded font-mono" /></Field><Field label="Grant types"><input required value={grants} onChange={e => setGrants(e.target.value)} placeholder="authorization_code refresh_token" style={inputStyle} className="w-full p-2 rounded" /></Field><Field label="Allowed resources"><input required value={resources} onChange={e => setResources(e.target.value)} placeholder="https://astonish.example/api/mcp" style={inputStyle} className="w-full p-2 rounded font-mono" /></Field><OAuthScopeSelector scopes={scopeOptions} value={scopes} onChange={setScopes} disabled={saving} /><label className="flex gap-2 text-sm" style={{ color: 'var(--text-secondary)' }}><input type="checkbox" checked={active} onChange={e => setActive(e.target.checked)} /> Allow new grants</label>{client?.client_type === 'confidential' && <label className="flex gap-2 text-sm" style={{ color: 'var(--text-secondary)' }}><input type="checkbox" checked={rotate} onChange={e => setRotate(e.target.checked)} /> Rotate the client secret (the old secret stops working)</label>}<div className="flex gap-3 pt-2"><button type="button" onClick={onCancel} disabled={saving} className="flex-1 p-2 rounded disabled:opacity-50" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button><button disabled={saving || !orgID || !teamID || scopes.length === 0} className="flex flex-1 justify-center gap-2 rounded p-2 text-white disabled:opacity-50" style={{ background: 'var(--brand)' }}>{saving ? <Loader2 size={16} className="animate-spin" /> : <RefreshCw size={16} />}{client ? 'Save' : 'Create'}</button></div></form></div>
}
function Endpoint({ label, value, onCopy }: { label: string; value: string; onCopy: (value: string, label: string) => Promise<void> }) { return <div className="grid grid-cols-[7rem_1fr_auto] gap-2 items-center"><dt style={{ color: 'var(--text-muted)' }}>{label}</dt><dd className="font-mono break-all" style={{ color: 'var(--text-secondary)' }}>{value}</dd><button onClick={() => void onCopy(value, label)} style={{ color: 'var(--brand)' }} title={`Copy ${label}`}><Copy size={14} /></button></div> }
function Loading() { return <div className="flex justify-center py-8"><Loader2 size={20} className="animate-spin" style={{ color: 'var(--text-muted)' }} /></div> }
function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="block text-sm"><span className="mb-1 block" style={{ color: 'var(--text-secondary)' }}>{label}</span>{children}</label> }
function Notice({ children, tone }: { children: ReactNode; tone: 'error' | 'success' }) { return <div className="rounded-lg p-3 text-sm" style={{ background: tone === 'error' ? 'var(--danger-soft)' : 'var(--success-soft)', color: tone === 'error' ? 'var(--danger)' : 'var(--success)' }}>{children}</div> }
