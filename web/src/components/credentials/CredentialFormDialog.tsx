import React, { useState } from 'react'
import { X } from 'lucide-react'
import { teamFetch } from '@/api/teamContext'
import { inputClass, inputStyle, labelStyle, hintStyle, saveButtonStyle } from '@/components/settings/settingsApi'
import {
  buildCredentialPayload,
  emptyCredForm,
  validateCredForm,
  type CredForm,
} from './credentialForm'

export type CredentialFormDialogProps = {
  open: boolean
  onClose: () => void
  /** When set, name field is locked (edit mode). */
  editingName?: string | null
  /** API scope query: personal | team | undefined (default store). */
  scope?: string
  /** Seed form when the dialog opens (suggested name/type, or revealed edit values). */
  initialForm?: Partial<CredForm>
  /** Dialog title override. */
  title?: string
  /** Optional subtitle under the title. */
  description?: string
  /** Primary button label. */
  saveLabel?: string
  /** Called after a successful save. */
  onSaved: (result: { name: string; type: string }) => void | Promise<void>
}

async function saveCredentialApi(
  name: string,
  credential: Record<string, unknown>,
  scope?: string,
): Promise<void> {
  const scopeParam = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/credentials${scopeParam}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, credential }),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    throw new Error((data as { error?: string }).error || 'Failed to save credential')
  }
}

/**
 * Full Add/Edit credential dialog shared by Settings → Credentials and MCP env bind.
 * Supports every credential type (api_key, bearer, basic, password, OAuth, Keystone, raw_content).
 *
 * Unmounts when closed so each open starts from a fresh form seeded by initialForm.
 */
export default function CredentialFormDialog(props: CredentialFormDialogProps) {
  if (!props.open) return null
  return <CredentialFormDialogInner {...props} />
}

function CredentialFormDialogInner({
  onClose,
  editingName = null,
  scope,
  initialForm,
  title,
  description,
  saveLabel = 'Save',
  onSaved,
}: CredentialFormDialogProps) {
  const [form, setForm] = useState<CredForm>(() => ({ ...emptyCredForm(), ...(initialForm || {}) }))
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const set = (patch: Partial<CredForm>) => setForm((prev) => ({ ...prev, ...patch }))

  const handleSave = async () => {
    const validationError = validateCredForm(form)
    if (validationError) {
      setError(validationError)
      return
    }
    setSaving(true)
    setError('')
    try {
      const name = form.name.trim()
      await saveCredentialApi(name, buildCredentialPayload(form), scope)
      await onSaved({ name, type: form.type })
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setSaving(false)
    }
  }

  const dialogTitle =
    title ||
    (editingName
      ? `Edit "${editingName}"`
      : `Add ${scope ? scope.charAt(0).toUpperCase() + scope.slice(1) + ' ' : ''}Credential`)

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'rgba(0,0,0,0.5)' }}
      onClick={onClose}
    >
      <div
        className="rounded-xl shadow-2xl p-6 w-full max-w-md max-h-[80vh] overflow-y-auto"
        style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
        onClick={(e: React.MouseEvent) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <div className="min-w-0 pr-2">
            <h3 className="text-base font-medium" style={{ color: 'var(--text-primary)' }}>
              {dialogTitle}
            </h3>
            {description ? (
              <p className="text-xs mt-1" style={hintStyle}>
                {description}
              </p>
            ) : null}
          </div>
          <button type="button" onClick={onClose} className="p-1 rounded hover:bg-white/10 shrink-0">
            <X size={16} style={{ color: 'var(--text-muted)' }} />
          </button>
        </div>

        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium mb-2" style={labelStyle}>
              Name
            </label>
            <input
              type="text"
              value={form.name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ name: e.target.value })}
              disabled={!!editingName}
              placeholder="my-api-key"
              className={inputClass + ' font-mono'}
              style={{ ...inputStyle, opacity: editingName ? 0.6 : 1 }}
              autoFocus={!editingName}
            />
          </div>

          <div>
            <label className="block text-sm font-medium mb-2" style={labelStyle}>
              Type
            </label>
            <select
              value={form.type}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => set({ type: e.target.value })}
              className={inputClass}
              style={inputStyle}
            >
              <option value="api_key">API Key (custom header + value)</option>
              <option value="bearer">Bearer Token</option>
              <option value="basic">Basic Auth (HTTP)</option>
              <option value="password">Password (SSH/FTP/SMTP/database)</option>
              <option value="oauth_client_credentials">OAuth Client Credentials</option>
              <option value="oauth_authorization_code">OAuth Authorization Code</option>
              <option value="openstack_keystone">OpenStack Keystone</option>
              <option value="raw_content">Raw Content (JSON/YAML/text file)</option>
            </select>
          </div>

          {form.type === 'api_key' && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Header Name
                </label>
                <input
                  type="text"
                  value={form.header}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ header: e.target.value })}
                  placeholder="Authorization"
                  className={inputClass}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Key Value
                </label>
                <input
                  type="password"
                  value={form.value}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ value: e.target.value })}
                  placeholder="sk-..."
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
            </>
          )}

          {form.type === 'bearer' && (
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>
                Token
              </label>
              <input
                type="password"
                value={form.token}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ token: e.target.value })}
                className={inputClass + ' font-mono'}
                style={inputStyle}
              />
            </div>
          )}

          {form.type === 'raw_content' && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Content Type <span className="font-normal" style={hintStyle}>(optional)</span>
                </label>
                <input
                  type="text"
                  value={form.content_type}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ content_type: e.target.value })}
                  placeholder="text/plain, application/json, application/yaml"
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Raw Content
                </label>
                <textarea
                  value={form.content}
                  onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => set({ content: e.target.value })}
                  placeholder="Paste JSON, YAML, dotenv, or text content"
                  className={inputClass + ' font-mono min-h-32'}
                  style={inputStyle}
                />
                <p className="text-xs mt-1" style={hintStyle}>
                  Stored encrypted and used via resolve_credential field content or fleet credential_injection field
                  content.
                </p>
              </div>
            </>
          )}

          {(form.type === 'basic' || form.type === 'password') && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Username
                </label>
                <input
                  type="text"
                  value={form.username}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ username: e.target.value })}
                  className={inputClass}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Password
                </label>
                <input
                  type="password"
                  value={form.password}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ password: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
            </>
          )}

          {form.type === 'oauth_client_credentials' && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Auth URL
                </label>
                <input
                  type="url"
                  value={form.auth_url}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ auth_url: e.target.value })}
                  placeholder="https://auth.example.com/oauth/token"
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Client ID
                </label>
                <input
                  type="text"
                  value={form.client_id}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ client_id: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Client Secret
                </label>
                <input
                  type="password"
                  value={form.client_secret}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ client_secret: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Scope <span className="font-normal" style={hintStyle}>(optional)</span>
                </label>
                <input
                  type="text"
                  value={form.scope}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ scope: e.target.value })}
                  className={inputClass}
                  style={inputStyle}
                />
              </div>
            </>
          )}

          {form.type === 'oauth_authorization_code' && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Token URL
                </label>
                <input
                  type="url"
                  value={form.token_url}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ token_url: e.target.value })}
                  placeholder="https://oauth2.googleapis.com/token"
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Client ID
                </label>
                <input
                  type="text"
                  value={form.client_id}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ client_id: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Client Secret
                </label>
                <input
                  type="password"
                  value={form.client_secret}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ client_secret: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Access Token
                </label>
                <input
                  type="password"
                  value={form.access_token}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ access_token: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Refresh Token
                </label>
                <input
                  type="password"
                  value={form.refresh_token}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ refresh_token: e.target.value })}
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Scope <span className="font-normal" style={hintStyle}>(optional)</span>
                </label>
                <input
                  type="text"
                  value={form.scope}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ scope: e.target.value })}
                  className={inputClass}
                  style={inputStyle}
                />
              </div>
            </>
          )}

          {form.type === 'openstack_keystone' && (
            <>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Auth URL
                </label>
                <input
                  type="url"
                  value={form.auth_url}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ auth_url: e.target.value })}
                  placeholder="https://identity.example.com/v3/auth/tokens"
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>
                  Auth Method
                </label>
                <select
                  value={form.keystone_method}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => set({ keystone_method: e.target.value })}
                  className={inputClass}
                  style={inputStyle}
                >
                  <option value="application_credential">Application Credential</option>
                  <option value="password">Password</option>
                </select>
              </div>
              {form.keystone_method === 'application_credential' ? (
                <>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Application Credential ID
                    </label>
                    <input
                      type="text"
                      value={form.application_credential_id}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                        set({ application_credential_id: e.target.value })
                      }
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Application Credential Secret
                    </label>
                    <input
                      type="password"
                      value={form.application_credential_secret}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                        set({ application_credential_secret: e.target.value })
                      }
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                  </div>
                </>
              ) : (
                <>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Username
                    </label>
                    <input
                      type="text"
                      value={form.username}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ username: e.target.value })}
                      className={inputClass}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Password
                    </label>
                    <input
                      type="password"
                      value={form.password}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ password: e.target.value })}
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      User Domain
                    </label>
                    <input
                      type="text"
                      value={form.user_domain}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ user_domain: e.target.value })}
                      placeholder="Default"
                      className={inputClass}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Project ID <span className="font-normal" style={hintStyle}>(or use project name below)</span>
                    </label>
                    <input
                      type="text"
                      value={form.project_id}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ project_id: e.target.value })}
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Project Name
                    </label>
                    <input
                      type="text"
                      value={form.project_name}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ project_name: e.target.value })}
                      className={inputClass}
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Project Domain
                    </label>
                    <input
                      type="text"
                      value={form.project_domain}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => set({ project_domain: e.target.value })}
                      placeholder="Default"
                      className={inputClass}
                      style={inputStyle}
                    />
                  </div>
                </>
              )}
            </>
          )}

          {error && (
            <p className="text-xs" style={{ color: '#ef4444' }}>
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-lg text-sm"
              style={{ color: 'var(--text-secondary)' }}
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void handleSave()}
              disabled={saving}
              className="px-4 py-1.5 rounded-lg text-sm text-white font-medium disabled:opacity-50"
              style={saveButtonStyle}
            >
              {saving ? 'Saving...' : saveLabel}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
