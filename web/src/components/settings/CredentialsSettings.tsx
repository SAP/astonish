import React, { useState, useEffect, useCallback } from 'react'
import { Plus, Trash2, Eye, EyeOff, Edit2, Shield, ShieldOff, Lock, AlertCircle, Check, Loader2, KeyRound, X, Copy, Upload, Download } from 'lucide-react'
import { inputClass, inputStyle, labelStyle, hintStyle, sectionBorderStyle, saveButtonStyle } from './settingsApi'
import { teamFetch } from '../../api/teamContext'
import CredentialFormDialog from '../credentials/CredentialFormDialog'
import { emptyCredForm, type CredForm } from '../credentials/credentialForm'

// --- Types ---

interface CredentialSummary {
  name: string
  type: string
  scope?: string    // "personal" | "team" (platform mode only)
  shadowed?: boolean // true if personal overrides this team credential
  [key: string]: unknown
}

interface SecretListItem {
  key: string
  scope?: string // "personal" | "team" (platform mode only)
}

interface RevealedCredential {
  type: string
  scope?: string
  header?: string
  value?: string
  token?: string
  username?: string
  password?: string
  auth_url?: string
  client_id?: string
  client_secret?: string
  oauth_scope?: string
  token_url?: string
  access_token?: string
  refresh_token?: string
  token_expiry?: string
  user_domain?: string
  project_id?: string
  project_name?: string
  project_domain?: string
  application_credential_id?: string
  application_credential_secret?: string
  content?: string
  content_type?: string
  [key: string]: unknown
}

interface CredentialsData {
  credentials: CredentialSummary[]
  secrets: SecretListItem[] | string[]
  has_master_key: boolean
  is_team_admin: boolean
}

interface SecretForm {
  key: string
  value: string
}

// API helpers — use teamFetch for platform-mode team header injection
const fetchCredentials = async (): Promise<CredentialsData> => {
  const res = await teamFetch('/api/credentials')
  if (!res.ok) throw new Error('Failed to fetch credentials')
  return res.json()
}

const revealCredential = async (name: string, masterKey: string | null, scope?: string): Promise<RevealedCredential> => {
  const headers: Record<string, string> = {}
  if (masterKey) headers['X-Master-Key'] = masterKey
  const scopeParam = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/credentials/${encodeURIComponent(name)}${scopeParam}`, { headers })
  if (res.status === 403) {
    const data = await res.json().catch(() => ({}))
    if (data.error === 'master_key_required') throw new Error('master_key_required')
    if (data.error === 'invalid_master_key') throw new Error('invalid_master_key')
    throw new Error(data.error || 'Access denied')
  }
  if (!res.ok) throw new Error('Failed to reveal credential')
  return res.json()
}

const revealSecret = async (key: string, masterKey: string | null, scope?: string): Promise<{ value: string }> => {
  const headers: Record<string, string> = {}
  if (masterKey) headers['X-Master-Key'] = masterKey
  const scopeParam = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/secrets/${encodeURIComponent(key)}${scopeParam}`, { headers })
  if (res.status === 403) {
    const data = await res.json().catch(() => ({}))
    if (data.error === 'master_key_required') throw new Error('master_key_required')
    if (data.error === 'invalid_master_key') throw new Error('invalid_master_key')
    throw new Error(data.error || 'Access denied')
  }
  if (!res.ok) throw new Error('Failed to reveal secret')
  return res.json()
}

const deleteCredentialApi = async (name: string, scope?: string): Promise<Record<string, unknown>> => {
  const scopeParam = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/credentials/${encodeURIComponent(name)}${scopeParam}`, { method: 'DELETE' })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.error || 'Failed to delete')
  }
  return res.json()
}

const saveSecretApi = async (key: string, value: string, scope?: string): Promise<Record<string, unknown>> => {
  const scopeParam = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/secrets/${encodeURIComponent(key)}${scopeParam}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value })
  })
  if (!res.ok) throw new Error('Failed to save secret')
  return res.json()
}

const publishCredentialApi = async (name: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/credentials/publish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.error || 'Failed to publish credential')
  }
  return res.json()
}

const forkCredentialApi = async (name: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/credentials/fork', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error(data.error || 'Failed to fork credential')
  }
  return res.json()
}

const apiSetMasterKey = async (current: string, newKey: string): Promise<Record<string, unknown>> => {
  const res = await fetch('/api/credentials/master-key', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ current, new: newKey })
  })
  if (res.status === 403) throw new Error('Invalid current master key')
  if (!res.ok) throw new Error('Failed to set master key')
  return res.json()
}

const verifyMasterKey = async (password: string): Promise<boolean> => {
  const res = await fetch('/api/credentials/verify-master-key', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password })
  })
  if (!res.ok) throw new Error('Failed to verify')
  const data = await res.json()
  return data.valid
}

// Credential type labels
const TYPE_LABELS: Record<string, string> = {
  api_key: 'API Key',
  bearer: 'Bearer Token',
  basic: 'Basic Auth',
  password: 'Password',
  oauth_client_credentials: 'OAuth Client Credentials',
  oauth_authorization_code: 'OAuth Auth Code',
  openstack_keystone: 'OpenStack Keystone',
  raw_content: 'Raw Content'
}

const TYPE_COLORS: Record<string, string> = {
  api_key: 'var(--brand)',
  bearer: '#3b82f6',
  basic: '#f59e0b',
  password: '#ef4444',
  oauth_client_credentials: '#10b981',
  oauth_authorization_code: '#06b6d4',
  openstack_keystone: '#f97316',
  raw_content: '#14b8a6'
}

const SCOPE_COLORS: Record<string, string> = {
  personal: 'var(--brand)',
  team: '#3b82f6',
}

// Type badge component
function TypeBadge({ type }: { type: string }) {
  const color = TYPE_COLORS[type] || '#6b7280'
  return (
    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium"
      style={{ background: color + '20', color, border: `1px solid ${color}40` }}>
      {TYPE_LABELS[type] || type}
    </span>
  )
}

// Scope badge component
function ScopeBadge({ scope }: { scope: string }) {
  const color = SCOPE_COLORS[scope] || '#6b7280'
  return (
    <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium uppercase tracking-wide"
      style={{ background: color + '15', color, border: `1px solid ${color}30` }}>
      {scope}
    </span>
  )
}

// Masked value display
function MaskedValue({ value, revealed }: { value: string | undefined; revealed: boolean }) {
  if (!revealed || !value) return <span className="font-mono text-xs" style={{ color: 'var(--text-muted)' }}>{'*'.repeat(Math.min(value?.length || 8, 32))}</span>
  return <span className="font-mono text-xs break-all" style={{ color: 'var(--text-primary)' }}>{value}</span>
}

// Copy button
function CopyButton({ text }: { text: string | undefined }) {
  const [copied, setCopied] = useState(false)
  const handleCopy = async (e: React.MouseEvent<HTMLButtonElement>) => {
    e.stopPropagation()
    await navigator.clipboard.writeText(text || '')
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  if (!text) return null
  return (
    <button onClick={handleCopy} className="p-1 rounded hover:bg-white/10 transition-colors" title="Copy">
      {copied ? <Check size={12} style={{ color: '#10b981' }} /> : <Copy size={12} style={{ color: 'var(--text-muted)' }} />}
    </button>
  )
}

// Normalize secrets from API (can be string[] or SecretListItem[])
function normalizeSecrets(secrets: SecretListItem[] | string[]): SecretListItem[] {
  if (!secrets || secrets.length === 0) return []
  if (typeof secrets[0] === 'string') {
    return (secrets as string[]).map(key => ({ key }))
  }
  return secrets as SecretListItem[]
}

export default function CredentialsSettings({ isPlatform: isPlatformProp }: { isPlatform?: boolean } = {}) {
  const [data, setData] = useState<CredentialsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [isPlatform, setIsPlatform] = useState(isPlatformProp ?? false)

  // Sync with prop when it changes (e.g., on mount the prop may arrive after initial render)
  useEffect(() => {
    if (isPlatformProp !== undefined) setIsPlatform(isPlatformProp)
  }, [isPlatformProp])

  // Master key state
  const [masterKey, setMasterKey] = useState<string | null>(null)
  const [showMasterKeyPrompt, setShowMasterKeyPrompt] = useState(false)
  const [masterKeyInput, setMasterKeyInput] = useState('')
  const [masterKeyError, setMasterKeyError] = useState('')
  const [masterKeyCallback, setMasterKeyCallback] = useState<((mk: string | null) => void) | null>(null)
  const [showMasterKeySetup, setShowMasterKeySetup] = useState(false)
  const [mkSetupCurrent, setMkSetupCurrent] = useState('')
  const [mkSetupNew, setMkSetupNew] = useState('')
  const [mkSetupConfirm, setMkSetupConfirm] = useState('')
  const [mkSetupError, setMkSetupError] = useState('')
  const [mkSetupSaving, setMkSetupSaving] = useState(false)

  // Revealed credentials/secrets
  const [revealedCreds, setRevealedCreds] = useState<Record<string, RevealedCredential>>({})
  const [revealedSecrets, setRevealedSecrets] = useState<Record<string, string>>({})
  const [revealingCred, setRevealingCred] = useState<string | null>(null)
  const [revealingSecret, setRevealingSecret] = useState<string | null>(null)

  // Add/edit modal (shared CredentialFormDialog)
  const [showCredModal, setShowCredModal] = useState(false)
  const [editingCred, setEditingCred] = useState<string | null>(null)
  const [credFormInitial, setCredFormInitial] = useState<Partial<CredForm>>({})
  const [addScope, setAddScope] = useState<string | undefined>(undefined)

  // Add secret modal
  const [showSecretModal, setShowSecretModal] = useState(false)
  const [secretForm, setSecretForm] = useState<SecretForm>({ key: '', value: '' })
  const [secretFormSaving, setSecretFormSaving] = useState(false)
  const [secretFormError, setSecretFormError] = useState('')
  const [addSecretScope, setAddSecretScope] = useState<string | undefined>(undefined)

  // Delete confirm
  const [deleteTarget, setDeleteTarget] = useState<{ name: string; scope?: string } | null>(null)
  const [deleting, setDeleting] = useState(false)

  // Publishing/forking
  const [publishing, setPublishing] = useState<string | null>(null)
  const [forking, setForking] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      const result = await fetchCredentials()
      setData(result)
      // Use prop if provided; otherwise detect platform mode from data
      if (!isPlatformProp) {
        const hasScopedCred = result.credentials?.some(c => c.scope)
        const hasScopedSecret = Array.isArray(result.secrets) && result.secrets.length > 0 && typeof result.secrets[0] === 'object' && 'scope' in (result.secrets[0] as unknown as Record<string, unknown>)
        setIsPlatform(hasScopedCred || hasScopedSecret)
      }
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }, [isPlatformProp])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const result = await fetchCredentials()
        if (cancelled) return
        setData(result)
        // Use prop if provided; otherwise detect platform mode from data
        if (!isPlatformProp) {
          const hasScopedCred = result.credentials?.some(c => c.scope)
          const hasScopedSecret = Array.isArray(result.secrets) && result.secrets.length > 0 && typeof result.secrets[0] === 'object' && 'scope' in (result.secrets[0] as unknown as Record<string, unknown>)
          setIsPlatform(hasScopedCred || hasScopedSecret)
        }
        setError(null)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Unknown error')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [isPlatformProp])

  // Master key prompt flow
  const withMasterKey = useCallback((callback: (mk: string | null) => void) => {
    if (!data?.has_master_key) {
      callback(null)
      return
    }
    if (masterKey) {
      callback(masterKey)
      return
    }
    setMasterKeyInput('')
    setMasterKeyError('')
    setMasterKeyCallback(() => callback)
    setShowMasterKeyPrompt(true)
  }, [data, masterKey])

  const handleMasterKeySubmit = async () => {
    if (!masterKeyInput) {
      setMasterKeyError('Please enter the master key')
      return
    }
    try {
      const valid = await verifyMasterKey(masterKeyInput)
      if (!valid) {
        setMasterKeyError('Invalid master key')
        return
      }
      setMasterKey(masterKeyInput)
      setShowMasterKeyPrompt(false)
      if (masterKeyCallback) masterKeyCallback(masterKeyInput)
    } catch {
      setMasterKeyError('Verification failed')
    }
  }

  // Reveal credential
  const handleRevealCred = (name: string, scope?: string) => {
    const key = scope ? `${scope}:${name}` : name
    if (revealedCreds[key]) {
      setRevealedCreds(prev => { const n = { ...prev }; delete n[key]; return n })
      return
    }
    withMasterKey(async (mk) => {
      try {
        setRevealingCred(key)
        const detail = await revealCredential(name, mk, scope)
        setRevealedCreds(prev => ({ ...prev, [key]: detail }))
      } catch (err) {
        if (err instanceof Error && (err.message === 'master_key_required' || err.message === 'invalid_master_key')) {
          setMasterKey(null)
          setMasterKeyInput('')
          setMasterKeyError(err.message === 'invalid_master_key' ? 'Master key expired or invalid' : '')
          setMasterKeyCallback(() => () => handleRevealCred(name, scope))
          setShowMasterKeyPrompt(true)
        } else {
          setError(err instanceof Error ? err.message : 'Unknown error')
        }
      } finally {
        setRevealingCred(null)
      }
    })
  }

  // Reveal secret
  const handleRevealSecret = (key: string, scope?: string) => {
    const sKey = scope ? `${scope}:${key}` : key
    if (revealedSecrets[sKey]) {
      setRevealedSecrets(prev => { const n = { ...prev }; delete n[sKey]; return n })
      return
    }
    withMasterKey(async (mk) => {
      try {
        setRevealingSecret(sKey)
        const detail = await revealSecret(key, mk, scope)
        setRevealedSecrets(prev => ({ ...prev, [sKey]: detail.value }))
      } catch (err) {
        if (err instanceof Error && (err.message === 'master_key_required' || err.message === 'invalid_master_key')) {
          setMasterKey(null)
          setMasterKeyInput('')
          setMasterKeyError(err.message === 'invalid_master_key' ? 'Master key expired or invalid' : '')
          setMasterKeyCallback(() => () => handleRevealSecret(key, scope))
          setShowMasterKeyPrompt(true)
        } else {
          setError(err instanceof Error ? err.message : 'Unknown error')
        }
      } finally {
        setRevealingSecret(null)
      }
    })
  }

  // Add credential
  const openAddCred = (scope?: string) => {
    setEditingCred(null)
    setAddScope(scope)
    setCredFormInitial({})
    setShowCredModal(true)
  }

  // Edit credential
  const openEditCred = (name: string, scope?: string) => {
    const key = scope ? `${scope}:${name}` : name
    const revealed = revealedCreds[key]
    if (!revealed) return
    setEditingCred(name)
    setAddScope(scope)
    const hasAppCred = !!(revealed.application_credential_id)
    setCredFormInitial({
      ...emptyCredForm(),
      name,
      type: revealed.type || 'api_key',
      header: revealed.header || 'Authorization',
      value: revealed.value || '',
      token: revealed.token || '',
      username: revealed.username || '',
      password: revealed.password || '',
      auth_url: revealed.auth_url || '',
      client_id: revealed.client_id || '',
      client_secret: revealed.client_secret || '',
      scope: revealed.oauth_scope || '',
      token_url: revealed.token_url || '',
      access_token: revealed.access_token || '',
      refresh_token: revealed.refresh_token || '',
      keystone_method: hasAppCred ? 'application_credential' : 'password',
      user_domain: revealed.user_domain || 'Default',
      project_id: revealed.project_id || '',
      project_name: revealed.project_name || '',
      project_domain: revealed.project_domain || 'Default',
      application_credential_id: revealed.application_credential_id || '',
      application_credential_secret: revealed.application_credential_secret || '',
      content: revealed.content || '',
      content_type: revealed.content_type || 'text/plain',
    })
    setShowCredModal(true)
  }

  // Blind edit for platform mode (overwrite without revealing current value)
  const openBlindEditCred = (name: string, type: string, scope?: string) => {
    setEditingCred(name)
    setAddScope(scope)
    setCredFormInitial({ ...emptyCredForm(), name, type: type || 'api_key' })
    setShowCredModal(true)
  }

  // Add secret
  const openAddSecret = (scope?: string) => {
    setSecretForm({ key: '', value: '' })
    setSecretFormError('')
    setAddSecretScope(scope)
    setShowSecretModal(true)
  }

  const handleSaveSecret = async () => {
    if (!secretForm.key) { setSecretFormError('Key is required'); return }
    if (!secretForm.value) { setSecretFormError('Value is required'); return }
    setSecretFormSaving(true)
    setSecretFormError('')
    try {
      await saveSecretApi(secretForm.key, secretForm.value, addSecretScope)
      setShowSecretModal(false)
      const sKey = addSecretScope ? `${addSecretScope}:${secretForm.key}` : secretForm.key
      setRevealedSecrets(prev => { const n = { ...prev }; delete n[sKey]; return n })
      await loadData()
    } catch (err) {
      setSecretFormError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setSecretFormSaving(false)
    }
  }

  // Delete
  const handleDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await deleteCredentialApi(deleteTarget.name, deleteTarget.scope)
      const key = deleteTarget.scope ? `${deleteTarget.scope}:${deleteTarget.name}` : deleteTarget.name
      setDeleteTarget(null)
      setRevealedCreds(prev => { const n = { ...prev }; delete n[key]; return n })
      setRevealedSecrets(prev => { const n = { ...prev }; delete n[key]; return n })
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setDeleting(false)
    }
  }

  // Publish (personal -> team)
  const handlePublish = async (name: string) => {
    setPublishing(name)
    try {
      await publishCredentialApi(name)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to publish')
    } finally {
      setPublishing(null)
    }
  }

  // Fork (team -> personal)
  const handleFork = async (name: string) => {
    setForking(name)
    try {
      await forkCredentialApi(name)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fork')
    } finally {
      setForking(null)
    }
  }

  // Master key setup
  const handleMasterKeySetup = async () => {
    setMkSetupError('')
    if (data?.has_master_key && !mkSetupCurrent) {
      setMkSetupError('Current master key is required')
      return
    }
    if (mkSetupNew && mkSetupNew !== mkSetupConfirm) {
      setMkSetupError('New keys do not match')
      return
    }
    setMkSetupSaving(true)
    try {
      await apiSetMasterKey(mkSetupCurrent, mkSetupNew)
      setShowMasterKeySetup(false)
      setMasterKey(null)
      setRevealedCreds({})
      setRevealedSecrets({})
      await loadData()
    } catch (err) {
      setMkSetupError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setMkSetupSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
        <span className="ml-2 text-sm" style={{ color: 'var(--text-muted)' }}>Loading credentials...</span>
      </div>
    )
  }

  const allCredentials = data?.credentials || []
  const allSecrets = normalizeSecrets(data?.secrets || [])
  const hasMK = data?.has_master_key || false

  // Split by scope for platform mode
  const personalCreds = isPlatform ? allCredentials.filter(c => c.scope === 'personal') : allCredentials
  const teamCreds = isPlatform ? allCredentials.filter(c => c.scope === 'team') : []
  const personalSecrets = isPlatform ? allSecrets.filter(s => s.scope === 'personal') : allSecrets
  const teamSecrets = isPlatform ? allSecrets.filter(s => s.scope === 'team') : []

  const renderCredentialRow = (cred: CredentialSummary) => {
    const credScope = cred.scope
    const key = credScope ? `${credScope}:${cred.name}` : cred.name
    const revealed = revealedCreds[key]
    const isRevealing = revealingCred === key
    const isTeam = credScope === 'team'
    const isPublishing = publishing === cred.name
    const isForking = forking === cred.name
    const isAdmin = data?.is_team_admin ?? false

    return (
      <div key={key} className="rounded-lg border p-3" style={{ borderColor: 'var(--border-color)', background: 'var(--bg-secondary)' }}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{cred.name}</span>
            <TypeBadge type={cred.type} />
            {cred.shadowed && (
              <span className="text-[10px] px-1.5 py-0.5 rounded" style={{ background: 'rgba(245, 158, 11, 0.15)', color: '#f59e0b', border: '1px solid rgba(245, 158, 11, 0.3)' }}>shadowed</span>
            )}
          </div>
          <div className="flex items-center gap-1">
            {/* Publish to team (personal only, admin only) */}
            {isPlatform && credScope === 'personal' && isAdmin && (
              <button
                onClick={() => handlePublish(cred.name)}
                className="p-1.5 rounded hover:bg-white/10 transition-colors"
                title="Publish to Team"
                disabled={isPublishing}
              >
                {isPublishing ? <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} /> :
                  <Upload size={14} style={{ color: 'var(--text-muted)' }} />}
              </button>
            )}
            {/* Fork to personal (team only, admin only) */}
            {isPlatform && isTeam && isAdmin && (
              <button
                onClick={() => handleFork(cred.name)}
                className="p-1.5 rounded hover:bg-white/10 transition-colors"
                title="Fork to Personal"
                disabled={isForking}
              >
                {isForking ? <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} /> :
                  <Download size={14} style={{ color: 'var(--text-muted)' }} />}
              </button>
            )}
            {/* Reveal/Hide (admin only for team credentials, disabled in platform mode) */}
            {!isPlatform && (!isTeam || isAdmin) && (
              <button
                onClick={() => handleRevealCred(cred.name, credScope)}
                className="p-1.5 rounded hover:bg-white/10 transition-colors"
                title={revealed ? 'Hide' : 'Reveal'}
                disabled={isRevealing}
              >
                {isRevealing ? <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} /> :
                  revealed ? <EyeOff size={14} style={{ color: 'var(--brand)' }} /> : <Eye size={14} style={{ color: 'var(--text-muted)' }} />}
              </button>
            )}
            {/* Edit: in platform mode always show (blind overwrite); in personal mode only when revealed */}
            {(isPlatform ? (!isTeam || isAdmin) : !!revealed) && (
              <button
                onClick={() => isPlatform ? openBlindEditCred(cred.name, cred.type, credScope) : openEditCred(cred.name, credScope)}
                className="p-1.5 rounded hover:bg-white/10 transition-colors"
                title="Edit"
              >
                <Edit2 size={14} style={{ color: 'var(--text-muted)' }} />
              </button>
            )}
            {/* Delete (admin only for team credentials) */}
            {(!isTeam || isAdmin) && (
              <button
                onClick={() => setDeleteTarget({ name: cred.name, scope: credScope })}
                className="p-1.5 rounded hover:bg-white/10 transition-colors"
                title="Delete"
              >
                <Trash2 size={14} style={{ color: 'var(--text-muted)' }} />
              </button>
            )}
          </div>
        </div>
        {/* Revealed fields (personal mode only) */}
        {!isPlatform && revealed && (
          <div className="mt-2 pt-2 border-t space-y-1.5" style={sectionBorderStyle}>
            {revealed.type === 'api_key' && (
              <>
                <FieldRow label="Header" value={revealed.header} />
                <FieldRow label="Value" value={revealed.value} secret />
              </>
            )}
            {revealed.type === 'bearer' && <FieldRow label="Token" value={revealed.token} secret />}
            {revealed.type === 'raw_content' && (
              <>
                {revealed.content_type && <FieldRow label="Content Type" value={revealed.content_type} />}
                <FieldRow label="Content" value={revealed.content} secret multiline />
              </>
            )}
            {(revealed.type === 'basic' || revealed.type === 'password') && (
              <>
                <FieldRow label="Username" value={revealed.username} />
                <FieldRow label="Password" value={revealed.password} secret />
              </>
            )}
            {revealed.type === 'oauth_client_credentials' && (
              <>
                <FieldRow label="Auth URL" value={revealed.auth_url} />
                <FieldRow label="Client ID" value={revealed.client_id} />
                <FieldRow label="Client Secret" value={revealed.client_secret} secret />
                {revealed.oauth_scope && <FieldRow label="Scope" value={revealed.oauth_scope} />}
              </>
            )}
            {revealed.type === 'oauth_authorization_code' && (
              <>
                <FieldRow label="Token URL" value={revealed.token_url} />
                <FieldRow label="Client ID" value={revealed.client_id} />
                <FieldRow label="Client Secret" value={revealed.client_secret} secret />
                <FieldRow label="Access Token" value={revealed.access_token} secret />
                <FieldRow label="Refresh Token" value={revealed.refresh_token} secret />
                {revealed.token_expiry && <FieldRow label="Token Expiry" value={revealed.token_expiry} />}
                {revealed.oauth_scope && <FieldRow label="Scope" value={revealed.oauth_scope} />}
              </>
            )}
            {revealed.type === 'openstack_keystone' && (
              <>
                <FieldRow label="Auth URL" value={revealed.auth_url} />
                {revealed.application_credential_id ? (
                  <>
                    <FieldRow label="App Credential ID" value={revealed.application_credential_id} />
                    <FieldRow label="App Credential Secret" value={revealed.application_credential_secret} secret />
                  </>
                ) : (
                  <>
                    <FieldRow label="Username" value={revealed.username} />
                    <FieldRow label="Password" value={revealed.password} secret />
                    {revealed.user_domain && <FieldRow label="User Domain" value={revealed.user_domain} />}
                    {revealed.project_id && <FieldRow label="Project ID" value={revealed.project_id} />}
                    {revealed.project_name && <FieldRow label="Project Name" value={revealed.project_name} />}
                    {revealed.project_domain && <FieldRow label="Project Domain" value={revealed.project_domain} />}
                  </>
                )}
              </>
            )}
          </div>
        )}
      </div>
    )
  }

  const renderSecretRow = (item: SecretListItem) => {
    const sScope = item.scope
    const sKey = sScope ? `${sScope}:${item.key}` : item.key
    const revealed = revealedSecrets[sKey]
    const isRevealing = revealingSecret === sKey
    const isTeamSecret = sScope === 'team'
    const isAdmin = data?.is_team_admin ?? false
    return (
      <div key={sKey} className="flex items-center justify-between px-3 py-2 rounded-lg" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <div className="flex-1 min-w-0 mr-3">
          <span className="font-mono text-xs" style={{ color: 'var(--text-primary)' }}>{item.key}</span>
          {!isPlatform && revealed !== undefined && (
            <div className="flex items-center gap-1 mt-0.5">
              <MaskedValue value={revealed} revealed={true} />
              <CopyButton text={revealed} />
            </div>
          )}
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          {/* Reveal/Hide (admin only for team secrets, disabled in platform mode) */}
          {!isPlatform && (!isTeamSecret || isAdmin) && (
            <button
              onClick={() => handleRevealSecret(item.key, sScope)}
              className="p-1.5 rounded hover:bg-white/10 transition-colors"
              title={revealed !== undefined ? 'Hide' : 'Reveal'}
              disabled={isRevealing}
            >
              {isRevealing ? <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} /> :
                revealed !== undefined ? <EyeOff size={14} style={{ color: 'var(--brand)' }} /> : <Eye size={14} style={{ color: 'var(--text-muted)' }} />}
            </button>
          )}
          {/* Delete (admin only for team secrets) */}
          {(!isTeamSecret || isAdmin) && (
            <button
              onClick={() => setDeleteTarget({ name: item.key, scope: sScope })}
              className="p-1.5 rounded hover:bg-white/10 transition-colors"
              title="Delete"
            >
              <Trash2 size={14} style={{ color: 'var(--text-muted)' }} />
            </button>
          )}
        </div>
      </div>
    )
  }

  const renderCredentialSection = (title: string, creds: CredentialSummary[], secrets: SecretListItem[], scope?: string) => {
    const isTeamScope = scope === 'team'
    const isAdmin = data?.is_team_admin ?? false
    const canManage = !isTeamScope || isAdmin
    return (
      <div>
        {/* Named Credentials */}
        <div>
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-sm font-medium flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
              {scope && <ScopeBadge scope={scope} />}
              {title} ({creds.length})
            </h3>
            {canManage && (
              <button
                onClick={() => openAddCred(scope)}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-white transition-all hover:scale-[1.02]"
                style={saveButtonStyle}
              >
                <Plus size={14} /> Add
              </button>
            )}
          </div>

          {creds.length === 0 ? (
            <div className="text-center py-6 rounded-lg" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
              <Lock size={20} className="mx-auto mb-2" style={{ color: 'var(--text-muted)' }} />
              <p className="text-sm" style={hintStyle}>No {scope ? `${scope} ` : ''}credentials stored.</p>
              {!scope && <p className="text-xs mt-1" style={hintStyle}>Add credentials via the button above, CLI, or chat.</p>}
            </div>
          ) : (
            <div className="space-y-2">
              {creds.map(renderCredentialRow)}
            </div>
          )}
        </div>

        {/* Flat Secrets */}
        <div className="mt-4">
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
              Secrets ({secrets.length})
            </h3>
            {canManage && (
              <button
                onClick={() => openAddSecret(scope)}
                className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-all hover:scale-[1.02]"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
              >
                <Plus size={12} /> Add
              </button>
            )}
          </div>

          {secrets.length === 0 ? (
            <div className="text-center py-4 rounded-lg" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
              <p className="text-xs" style={hintStyle}>No {scope ? `${scope} ` : ''}secrets stored.</p>
            </div>
          ) : (
            <div className="space-y-1">
              {secrets.map(renderSecretRow)}
            </div>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="w-full space-y-6">
      {error && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={{ background: 'rgba(239, 68, 68, 0.1)', border: '1px solid rgba(239, 68, 68, 0.3)' }}>
          <AlertCircle size={16} style={{ color: '#ef4444' }} />
          <span style={{ color: '#ef4444' }}>{error}</span>
          <button onClick={() => setError(null)} className="ml-auto"><X size={14} style={{ color: '#ef4444' }} /></button>
        </div>
      )}

      {/* Master Key Banner (personal mode only) */}
      {!isPlatform && (
        <div className="flex items-center justify-between p-3 rounded-lg" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <div className="flex items-center gap-3">
            {hasMK ? (
              <>
                <Shield size={18} style={{ color: '#10b981' }} />
                <div>
                  <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Master key enabled</div>
                  <div className="text-xs" style={hintStyle}>Credential values require the master key to view.</div>
                </div>
              </>
            ) : (
              <>
                <ShieldOff size={18} style={{ color: 'var(--text-muted)' }} />
                <div>
                  <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>No master key</div>
                  <div className="text-xs" style={hintStyle}>Credential values can be viewed freely. Set a master key for extra protection.</div>
                </div>
              </>
            )}
          </div>
          <button
            onClick={() => {
              setMkSetupCurrent('')
              setMkSetupNew('')
              setMkSetupConfirm('')
              setMkSetupError('')
              setShowMasterKeySetup(true)
            }}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all hover:scale-[1.02]"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
          >
            <KeyRound size={14} />
            {hasMK ? 'Change' : 'Set Master Key'}
          </button>
        </div>
      )}

      {/* Platform mode: two sections */}
      {isPlatform ? (
        <>
          {/* Personal Credentials Section */}
          <div className="rounded-xl p-4" style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}>
            <div className="mb-3">
              <p className="text-xs" style={hintStyle}>
                Your private credentials. Only you can see and use these. Credentials saved from chat go here by default.
              </p>
            </div>
            {renderCredentialSection('Personal Credentials', personalCreds, personalSecrets, 'personal')}
          </div>

          {/* Team Credentials Section */}
          <div className="rounded-xl p-4" style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}>
            <div className="mb-3">
              <p className="text-xs" style={hintStyle}>
                Shared team credentials for app-to-app integrations. All members can use these; only admins can view values or edit.
              </p>
            </div>
            {renderCredentialSection('Team Credentials', teamCreds, teamSecrets, 'team')}
          </div>
        </>
      ) : (
        // Personal mode: single section (no scope labels)
        renderCredentialSection('Credentials', personalCreds, personalSecrets)
      )}

      {/* Master Key Prompt Modal */}
      {showMasterKeyPrompt && (
        <Modal onClose={() => setShowMasterKeyPrompt(false)} title="Master Key Required">
          <div className="space-y-4">
            <p className="text-sm" style={hintStyle}>Enter the master key to view credential values.</p>
            <input
              type="password"
              value={masterKeyInput}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setMasterKeyInput(e.target.value)}
              onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => e.key === 'Enter' && handleMasterKeySubmit()}
              placeholder="Master key"
              className={inputClass}
              style={inputStyle}
              autoFocus
            />
            {masterKeyError && <p className="text-xs" style={{ color: '#ef4444' }}>{masterKeyError}</p>}
            <div className="flex justify-end gap-2">
              <button onClick={() => setShowMasterKeyPrompt(false)} className="px-3 py-1.5 rounded-lg text-sm" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
              <button onClick={handleMasterKeySubmit} className="px-4 py-1.5 rounded-lg text-sm text-white font-medium" style={saveButtonStyle}>Unlock</button>
            </div>
          </div>
        </Modal>
      )}

      {/* Master Key Setup Modal */}
      {showMasterKeySetup && (
        <Modal onClose={() => setShowMasterKeySetup(false)} title={hasMK ? 'Change Master Key' : 'Set Master Key'}>
          <div className="space-y-4">
            {hasMK && (
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>Current Master Key</label>
                <input type="password" value={mkSetupCurrent} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setMkSetupCurrent(e.target.value)} className={inputClass} style={inputStyle} />
              </div>
            )}
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>New Master Key {hasMK && <span className="font-normal" style={hintStyle}>(empty to remove)</span>}</label>
              <input type="password" value={mkSetupNew} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setMkSetupNew(e.target.value)} className={inputClass} style={inputStyle} />
            </div>
            {mkSetupNew && (
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>Confirm Master Key</label>
                <input type="password" value={mkSetupConfirm} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setMkSetupConfirm(e.target.value)} className={inputClass} style={inputStyle} />
              </div>
            )}
            {mkSetupError && <p className="text-xs" style={{ color: '#ef4444' }}>{mkSetupError}</p>}
            <div className="flex justify-end gap-2">
              <button onClick={() => setShowMasterKeySetup(false)} className="px-3 py-1.5 rounded-lg text-sm" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
              <button onClick={handleMasterKeySetup} disabled={mkSetupSaving} className="px-4 py-1.5 rounded-lg text-sm text-white font-medium disabled:opacity-50" style={saveButtonStyle}>
                {mkSetupSaving ? 'Saving...' : mkSetupNew ? 'Set Key' : 'Remove Key'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {/* Add/Edit Credential Modal — shared with MCP env bind */}
      <CredentialFormDialog
        open={showCredModal}
        onClose={() => setShowCredModal(false)}
        editingName={editingCred}
        scope={addScope}
        initialForm={credFormInitial}
        onSaved={async ({ name }) => {
          const key = addScope ? `${addScope}:${name}` : name
          setRevealedCreds((prev) => {
            const n = { ...prev }
            delete n[key]
            return n
          })
          await loadData()
        }}
      />

      {/* Add Secret Modal */}
      {showSecretModal && (
        <Modal onClose={() => setShowSecretModal(false)} title={`Add ${addSecretScope ? addSecretScope.charAt(0).toUpperCase() + addSecretScope.slice(1) + ' ' : ''}Secret`}>
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>Key</label>
              <input type="text" value={secretForm.key} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSecretForm({ ...secretForm, key: e.target.value })} placeholder="provider.example.api_key" className={inputClass + ' font-mono'} style={inputStyle} />
              <p className="text-xs mt-1" style={hintStyle}>Dot-notation key (e.g., provider.openai.api_key)</p>
            </div>
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>Value</label>
              <input type="password" value={secretForm.value} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSecretForm({ ...secretForm, value: e.target.value })} className={inputClass + ' font-mono'} style={inputStyle} />
            </div>
            {secretFormError && <p className="text-xs" style={{ color: '#ef4444' }}>{secretFormError}</p>}
            <div className="flex justify-end gap-2">
              <button onClick={() => setShowSecretModal(false)} className="px-3 py-1.5 rounded-lg text-sm" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
              <button onClick={handleSaveSecret} disabled={secretFormSaving} className="px-4 py-1.5 rounded-lg text-sm text-white font-medium disabled:opacity-50" style={saveButtonStyle}>
                {secretFormSaving ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {/* Delete Confirm Modal */}
      {deleteTarget && (
        <Modal onClose={() => setDeleteTarget(null)} title="Delete Credential">
          <div className="space-y-4">
            <p className="text-sm" style={{ color: 'var(--text-primary)' }}>
              Are you sure you want to delete <span className="font-mono font-medium">{deleteTarget.name}</span>
              {deleteTarget.scope && <> from <span className="font-medium">{deleteTarget.scope}</span> store</>}?
            </p>
            <p className="text-xs" style={hintStyle}>This action cannot be undone. Any tools or configurations referencing this credential will stop working.</p>
            <div className="flex justify-end gap-2">
              <button onClick={() => setDeleteTarget(null)} className="px-3 py-1.5 rounded-lg text-sm" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
              <button onClick={handleDelete} disabled={deleting} className="px-4 py-1.5 rounded-lg text-sm text-white font-medium disabled:opacity-50" style={{ background: '#ef4444' }}>
                {deleting ? 'Deleting...' : 'Delete'}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}

// Reusable field row for revealed credentials
function FieldRow({ label, value, secret, multiline }: { label: string; value: string | undefined; secret?: boolean; multiline?: boolean }) {
  const [show, setShow] = useState(!secret)
  const displayValue = show ? value : '*'.repeat(Math.min(value?.length || 8, 32))
  return (
    <div className={multiline ? 'space-y-1.5' : 'flex items-center gap-2'}>
      <span className="text-xs w-28 flex-shrink-0" style={hintStyle}>{label}</span>
      {multiline ? (
        <pre className="font-mono text-xs whitespace-pre-wrap break-all rounded p-2 max-h-48 overflow-auto" style={{ color: 'var(--text-primary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}>
          {displayValue}
        </pre>
      ) : (
        <span className="font-mono text-xs break-all flex-1" style={{ color: 'var(--text-primary)' }}>
          {displayValue}
        </span>
      )}
      <div className={multiline ? 'flex items-center gap-1' : 'contents'}>
        {secret && (
          <button onClick={() => setShow(!show)} className="p-0.5 flex-shrink-0">
            {show ? <EyeOff size={12} style={{ color: 'var(--text-muted)' }} /> : <Eye size={12} style={{ color: 'var(--text-muted)' }} />}
          </button>
        )}
        {show && <CopyButton text={value} />}
      </div>
    </div>
  )
}

// Simple modal wrapper
function Modal({ children, onClose, title }: { children: React.ReactNode; onClose: () => void; title: string }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" style={{ background: 'rgba(0,0,0,0.5)' }} onClick={onClose}>
      <div className="rounded-xl shadow-2xl p-6 w-full max-w-md max-h-[80vh] overflow-y-auto" style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }} onClick={(e: React.MouseEvent) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-base font-medium" style={{ color: 'var(--text-primary)' }}>{title}</h3>
          <button onClick={onClose} className="p-1 rounded hover:bg-white/10"><X size={16} style={{ color: 'var(--text-muted)' }} /></button>
        </div>
        {children}
      </div>
    </div>
  )
}
