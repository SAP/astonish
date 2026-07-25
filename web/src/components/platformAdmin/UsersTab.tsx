import { useState, useEffect, useCallback, type FormEvent, type ChangeEvent } from 'react'
import { Trash2, Loader2, Crown, UserPlus, Search, Edit2, CheckCircle2, Ban } from 'lucide-react'
import * as adminApi from '../../api/platformAdmin'
import type { AdminUser } from '../../api/platformAdmin'
import { InlineError, InlineSuccess, StatusBadge, RoleBadge } from './shared'
import { gradientAmber, inputStyle } from './sharedStyles'

// ---------------------------------------------------------------------------
// Users Tab
// ---------------------------------------------------------------------------

export default function UsersTab() {
  const [users, setUsers] = useState<AdminUser[]>([])
  const [loading, setLoading] = useState<boolean>(true)
  const [error, setError] = useState<string>('')
  const [success, setSuccess] = useState<string>('')
  const [showCreate, setShowCreate] = useState<boolean>(false)
  const [filter, setFilter] = useState<string>('')
  const [editingUser, setEditingUser] = useState<AdminUser | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const data = await adminApi.listUsers()
      setUsers(data)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  // Auto-dismiss
  useEffect(() => {
    if (success) { const t = setTimeout(() => setSuccess(''), 3000); return () => clearTimeout(t) }
  }, [success])
  useEffect(() => {
    if (error) { const t = setTimeout(() => setError(''), 5000); return () => clearTimeout(t) }
  }, [error])

  const filtered = users.filter(u =>
    u.email.toLowerCase().includes(filter.toLowerCase()) ||
    u.display_name.toLowerCase().includes(filter.toLowerCase())
  )

  const handleDelete = async (user: AdminUser) => {
    if (!confirm(`Delete user "${user.email}"? This action cannot be undone.`)) return
    try {
      await adminApi.deleteUser(user.id)
      setSuccess(`User "${user.email}" deleted`)
      load()
    } catch (e) { setError((e as Error).message) }
  }

  const handleToggleSuperadmin = async (user: AdminUser) => {
    const newRole = user.platform_role === 'superadmin' ? '' : 'superadmin'
    const action = newRole === 'superadmin' ? 'Promote' : 'Demote'
    if (!confirm(`${action} "${user.email}" ${newRole ? 'to' : 'from'} platform superadmin?`)) return
    try {
      await adminApi.updateUser(user.id, { platform_role: newRole })
      setSuccess(`User "${user.email}" ${action.toLowerCase()}d`)
      load()
    } catch (e) { setError((e as Error).message) }
  }

  const handleToggleStatus = async (user: AdminUser) => {
    const newStatus = user.status === 'active' ? 'suspended' : 'active'
    try {
      await adminApi.updateUser(user.id, { status: newStatus })
      setSuccess(`User "${user.email}" ${newStatus === 'active' ? 'reactivated' : 'suspended'}`)
      load()
    } catch (e) { setError((e as Error).message) }
  }

  return (
    <>
      {/* Status messages */}
      {(error || success) && (
        <div className="px-6 pt-3">
          {error && <InlineError msg={error} />}
          {success && <InlineSuccess msg={success} />}
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-6 py-4">
        {/* Filter + action bar */}
        <div className="flex items-center gap-3 mb-4">
          <div className="flex-1 relative">
            <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2" style={{ color: 'var(--text-muted)' }} />
            <input
              type="text"
              placeholder="Filter users..."
              value={filter}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setFilter(e.target.value)}
              className="w-full pl-9 pr-3 py-2 rounded-xl text-sm outline-none"
              style={inputStyle}
            />
          </div>
          <button
            onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-4 py-2 rounded-xl text-sm font-medium text-white hover:opacity-90"
            style={gradientAmber}
          >
            <UserPlus size={14} /> New User
          </button>
        </div>

        {/* Table */}
        {loading ? (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
          </div>
        ) : filtered.length === 0 ? (
          <p className="text-center py-12 text-sm" style={{ color: 'var(--text-muted)' }}>
            {users.length === 0 ? 'No users yet.' : 'No matches.'}
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr style={{ color: 'var(--text-muted)' }}>
                <th className="text-left py-2 px-3 font-medium">User</th>
                <th className="text-left py-2 px-3 font-medium">Status</th>
                <th className="text-left py-2 px-3 font-medium">Platform Role</th>
                <th className="text-left py-2 px-3 font-medium">Organizations</th>
                <th className="text-left py-2 px-3 font-medium">Joined</th>
                <th className="text-right py-2 px-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(u => (
                <tr key={u.id} className="border-t" style={{ borderColor: 'var(--border-color)' }}>
                  <td className="py-3 px-3">
                    <div className="font-medium" style={{ color: 'var(--text-primary)' }}>{u.display_name}</div>
                    <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{u.email}</div>
                  </td>
                  <td className="py-3 px-3"><StatusBadge status={u.status} /></td>
                  <td className="py-3 px-3">
                    {u.platform_role === 'superadmin' ? (
                      <RoleBadge role="superadmin" />
                    ) : (
                      <span className="text-xs" style={{ color: 'var(--text-muted)' }}>--</span>
                    )}
                  </td>
                  <td className="py-3 px-3">
                    <div className="flex flex-wrap gap-1">
                      {u.orgs && u.orgs.length > 0 ? u.orgs.map((o, i) => (
                        <RoleBadge key={i} role={o.role || 'member'} />
                      )) : (
                        <span className="text-xs" style={{ color: 'var(--text-muted)' }}>None</span>
                      )}
                    </div>
                  </td>
                  <td className="py-3 px-3" style={{ color: 'var(--text-muted)' }}>
                    {new Date(u.created_at).toLocaleDateString()}
                  </td>
                  <td className="py-3 px-3">
                    <div className="flex items-center justify-end gap-1">
                      <button onClick={() => setEditingUser(u)} className="p-1.5 rounded-lg transition-opacity hover:opacity-80" style={{ color: 'var(--brand)' }} title="Edit">
                        <Edit2 size={14} />
                      </button>
                      <button onClick={() => handleToggleSuperadmin(u)} className="p-1.5 rounded-lg transition-opacity hover:opacity-80" style={{ color: u.platform_role === 'superadmin' ? '#eab308' : 'var(--text-muted)' }} title={u.platform_role === 'superadmin' ? 'Demote from Superadmin' : 'Promote to Superadmin'}>
                        <Crown size={14} />
                      </button>
                      <button onClick={() => handleToggleStatus(u)} className="p-1.5 rounded-lg transition-opacity hover:opacity-80" style={{ color: u.status === 'active' ? '#f59e0b' : '#22c55e' }} title={u.status === 'active' ? 'Suspend' : 'Reactivate'}>
                        {u.status === 'active' ? <Ban size={14} /> : <CheckCircle2 size={14} />}
                      </button>
                      <button onClick={() => handleDelete(u)} className="p-1.5 rounded-lg transition-opacity hover:opacity-80" style={{ color: 'var(--danger)' }} title="Delete">
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Create user modal */}
      {showCreate && (
        <CreateUserModal
          onCreated={() => { setShowCreate(false); load() }}
          onCancel={() => setShowCreate(false)}
          onError={setError}
          onSuccess={setSuccess}
        />
      )}

      {/* Edit user modal */}
      {editingUser && (
        <EditUserModal
          user={editingUser}
          onSaved={() => { setEditingUser(null); load() }}
          onCancel={() => setEditingUser(null)}
          onError={setError}
          onSuccess={setSuccess}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// Create User Modal
// ---------------------------------------------------------------------------

interface CreateUserModalProps {
  onCreated: () => void
  onCancel: () => void
  onError: (msg: string) => void
  onSuccess: (msg: string) => void
}

function CreateUserModal({ onCreated, onCancel, onError, onSuccess }: CreateUserModalProps) {
  const [email, setEmail] = useState<string>('')
  const [displayName, setDisplayName] = useState<string>('')
  const [password, setPassword] = useState<string>('')
  const [submitting, setSubmitting] = useState<boolean>(false)
  const [localError, setLocalError] = useState<string>('')

  const handleSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!email.trim() || !displayName.trim()) {
      setLocalError('Email and display name are required')
      return
    }
    setSubmitting(true); setLocalError('')
    try {
      const result = await adminApi.createUser({
        email: email.trim(),
        display_name: displayName.trim(),
        password: password.trim() || undefined,
      })
      onSuccess(result.message)
      onCreated()
    } catch (e) {
      setLocalError((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onCancel} />
      <div className="relative w-full max-w-md mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
        <div className="px-6 py-5" style={gradientAmber}>
          <h2 className="text-lg font-semibold text-white">Create User</h2>
          <p className="text-sm text-white/70 mt-0.5">Add a new user to the platform</p>
        </div>
        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          <InlineError msg={localError} />
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Email *</label>
            <input type="email" value={email} onChange={(e: ChangeEvent<HTMLInputElement>) => setEmail(e.target.value)} placeholder="alice@acme.com" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} autoFocus />
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Display Name *</label>
            <input type="text" value={displayName} onChange={(e: ChangeEvent<HTMLInputElement>) => setDisplayName(e.target.value)} placeholder="Alice Smith" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} />
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Password <span className="text-xs font-normal" style={{ color: 'var(--text-muted)' }}>(optional)</span></label>
            <input type="password" value={password} onChange={(e: ChangeEvent<HTMLInputElement>) => setPassword(e.target.value)} placeholder="Leave empty for SSO-only users" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} />
            <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>If empty, user can only log in via configured SSO provider.</p>
          </div>
          <p className="text-xs" style={{ color: 'var(--text-muted)' }}>To assign the user to an organization, go to the org's user management page after creation.</p>
          <div className="flex gap-3 pt-2">
            <button type="button" onClick={onCancel} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
            <button type="submit" disabled={submitting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={gradientAmber}>
              {submitting ? <Loader2 size={16} className="animate-spin" /> : 'Create User'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Edit User Modal
// ---------------------------------------------------------------------------

interface EditUserModalProps {
  user: AdminUser
  onSaved: () => void
  onCancel: () => void
  onError: (msg: string) => void
  onSuccess: (msg: string) => void
}

function EditUserModal({ user, onSaved, onCancel, onError, onSuccess }: EditUserModalProps) {
  const [displayName, setDisplayName] = useState<string>(user.display_name)
  const [password, setPassword] = useState<string>('')
  const [submitting, setSubmitting] = useState<boolean>(false)
  const [localError, setLocalError] = useState<string>('')

  const handleSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setSubmitting(true); setLocalError('')
    try {
      const params: { display_name?: string; password?: string } = {}
      if (displayName !== user.display_name) params.display_name = displayName
      if (password) params.password = password
      if (Object.keys(params).length === 0) { onCancel(); return }
      await adminApi.updateUser(user.id, params)
      onSuccess(`User "${user.email}" updated`)
      onSaved()
    } catch (e) {
      setLocalError((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onCancel} />
      <div className="relative w-full max-w-sm mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
        <div className="px-6 py-5" style={{ background: 'linear-gradient(135deg, #3b82f6 0%, #2563eb 100%)' }}>
          <h2 className="text-lg font-semibold text-white">Edit User</h2>
          <p className="text-sm text-white/70 mt-0.5">{user.email}</p>
        </div>
        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          <InlineError msg={localError} />
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Display Name</label>
            <input type="text" value={displayName} onChange={(e: ChangeEvent<HTMLInputElement>) => setDisplayName(e.target.value)} className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} autoFocus />
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>New Password <span className="text-xs font-normal" style={{ color: 'var(--text-muted)' }}>(leave blank to keep current)</span></label>
            <input type="password" value={password} onChange={(e: ChangeEvent<HTMLInputElement>) => setPassword(e.target.value)} placeholder="Minimum 8 characters" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} />
          </div>
          <div className="flex gap-3 pt-2">
            <button type="button" onClick={onCancel} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
            <button type="submit" disabled={submitting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={{ background: 'linear-gradient(135deg, #3b82f6 0%, #2563eb 100%)' }}>
              {submitting ? <Loader2 size={16} className="animate-spin" /> : 'Save Changes'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
