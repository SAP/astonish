import React, { useState, useEffect, useCallback } from 'react'
import { Users, UserPlus, Shield, ShieldCheck, AlertCircle, Loader2, Trash2, KeyRound, Ban, CheckCircle2 } from 'lucide-react'
import {
  fetchOrgUsers, setUserOrgRole, setUserStatus, deleteOrgUser, resetUserPassword, inviteUserToOrg,
  type OrgUser,
} from '../api/platform'

interface UserManagementProps {
  theme: 'dark' | 'light'
  user: { id: string; email: string; display_name: string; role: string }
  org: { id: string; name: string; slug: string }
}

const errMsg = (err: unknown, fallback: string) => err instanceof Error ? err.message : fallback
const gradientBlue = { background: 'linear-gradient(135deg, #3b82f6 0%, #2563eb 100%)' }
const inputStyle = { background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }
const errorBg = { background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)', border: '1px solid rgba(239, 68, 68, 0.2)' }
const successBg = { background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e', border: '1px solid rgba(34, 197, 94, 0.2)' }

function InlineError({ msg }: { msg: string }) {
  if (!msg) return null
  return (
    <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={errorBg}>
      <AlertCircle size={14} /><span>{msg}</span>
    </div>
  )
}

function InlineSuccess({ msg }: { msg: string }) {
  if (!msg) return null
  return (
    <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={successBg}>
      <CheckCircle2 size={14} /><span>{msg}</span>
    </div>
  )
}

function StatusBadge({ status }: { status: string }) {
  const isActive = status === 'active'
  return (
    <span
      className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium"
      style={{
        background: isActive ? 'rgba(34, 197, 94, 0.15)' : 'rgba(239, 68, 68, 0.15)',
        color: isActive ? '#22c55e' : '#ef4444',
      }}
    >
      <span className="w-1.5 h-1.5 rounded-full" style={{ background: isActive ? '#22c55e' : '#ef4444' }} />
      {status}
    </span>
  )
}

function RoleBadge({ role }: { role: string }) {
  const colors: Record<string, { bg: string; fg: string }> = {
    owner: { bg: 'rgba(234, 179, 8, 0.15)', fg: '#eab308' },
    admin: { bg: 'var(--brand-muted)', fg: 'var(--brand)' },
    member: { bg: 'rgba(59, 130, 246, 0.15)', fg: '#3b82f6' },
  }
  const c = colors[role] || colors.member
  return (
    <span className="px-2 py-0.5 rounded-full text-xs font-medium" style={{ background: c.bg, color: c.fg }}>
      {role}
    </span>
  )
}

export default function UserManagement({ user, org }: UserManagementProps) {
  const [users, setUsers] = useState<OrgUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [actionError, setActionError] = useState('')

  // Password reset modal state
  const [resetTarget, setResetTarget] = useState<OrgUser | null>(null)
  const [newPassword, setNewPassword] = useState('')
  const [resetting, setResetting] = useState(false)
  const [resetError, setResetError] = useState('')

  // Delete confirmation modal state
  const [deleteTarget, setDeleteTarget] = useState<OrgUser | null>(null)
  const [deleting, setDeleting] = useState(false)

  // Invite user modal state
  const [showInvite, setShowInvite] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteDisplayName, setInviteDisplayName] = useState('')
  const [inviteRole, setInviteRole] = useState('member')
  const [inviteSendEmail, setInviteSendEmail] = useState(true)
  const [inviting, setInviting] = useState(false)
  const [inviteError, setInviteError] = useState('')

  const isOwner = user.role === 'owner'

  const loadUsers = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const data = await fetchOrgUsers()
      setUsers(data)
    } catch (err) { setError(errMsg(err, 'Failed to load users')) }
    finally { setLoading(false) }
  }, [])

   
  useEffect(() => { void loadUsers() }, [loadUsers])

  // Auto-dismiss success after 3s
  useEffect(() => {
    if (success) {
      const t = setTimeout(() => setSuccess(''), 3000)
      return () => clearTimeout(t)
    }
  }, [success])

  // Auto-dismiss action error after 5s
  useEffect(() => {
    if (actionError) {
      const t = setTimeout(() => setActionError(''), 5000)
      return () => clearTimeout(t)
    }
  }, [actionError])

  const handleRoleChange = async (targetId: string, role: string) => {
    setActionError('')
    try {
      await setUserOrgRole(targetId, role)
      setSuccess(`Role updated to ${role}`)
      await loadUsers()
    } catch (err) { setActionError(errMsg(err, 'Failed to update role')) }
  }

  const handleToggleStatus = async (target: OrgUser) => {
    setActionError('')
    const newStatus = target.status === 'active' ? 'disabled' : 'active'
    try {
      await setUserStatus(target.id, newStatus)
      setSuccess(`User ${newStatus === 'active' ? 'enabled' : 'disabled'}`)
      await loadUsers()
    } catch (err) { setActionError(errMsg(err, 'Failed to update status')) }
  }

  const handleResetPassword = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!resetTarget) return
    setResetting(true); setResetError('')
    try {
      await resetUserPassword(resetTarget.id, newPassword)
      setResetTarget(null); setNewPassword('')
      setSuccess('Password reset successfully')
    } catch (err) { setResetError(errMsg(err, 'Failed to reset password')) }
    finally { setResetting(false) }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true); setActionError('')
    try {
      await deleteOrgUser(deleteTarget.id)
      setDeleteTarget(null)
      setSuccess('User removed from organization')
      await loadUsers()
    } catch (err) { setActionError(errMsg(err, 'Failed to remove user')) }
    finally { setDeleting(false) }
  }

  const handleInvite = async (e: React.FormEvent) => {
    e.preventDefault()
    setInviting(true); setInviteError('')
    try {
      const result = await inviteUserToOrg({
        email: inviteEmail,
        display_name: inviteDisplayName,
        role: inviteRole,
        send_invite: inviteSendEmail,
      })
      setShowInvite(false)
      setInviteEmail(''); setInviteDisplayName(''); setInviteRole('member'); setInviteSendEmail(true)
      setSuccess(result.created ? `User created and added to ${org.name}` : `User added to ${org.name}`)
      await loadUsers()
    } catch (err) { setInviteError(errMsg(err, 'Failed to add user')) }
    finally { setInviting(false) }
  }

  return (
    <div className="flex flex-col h-full" style={{ background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
      {/* Header */}
      <div className="flex items-center justify-between px-6 py-4 border-b" style={{ borderColor: 'var(--border-color)' }}>
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-xl" style={gradientBlue}>
            <Users size={20} className="text-white" />
          </div>
          <div>
            <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>User Management</h1>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{org.name} &middot; {users.length} user{users.length !== 1 ? 's' : ''}</p>
          </div>
        </div>
        <button
          onClick={() => setShowInvite(true)}
          className="flex items-center gap-2 px-4 py-2 rounded-xl text-sm font-medium text-white hover:opacity-90 transition-opacity"
          style={gradientBlue}
        >
          <UserPlus size={16} />
          Add User
        </button>
      </div>

      {/* Status messages */}
      {(actionError || success) && (
        <div className="px-6 pt-3">
          {actionError && <InlineError msg={actionError} />}
          {success && <InlineSuccess msg={success} />}
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-6 py-4">
        {loading ? (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
          </div>
        ) : error ? (
          <InlineError msg={error} />
        ) : users.length === 0 ? (
          <p className="text-center py-12 text-sm" style={{ color: 'var(--text-muted)' }}>No users found.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr style={{ color: 'var(--text-muted)' }}>
                <th className="text-left py-2 px-3 font-medium">User</th>
                <th className="text-left py-2 px-3 font-medium">Status</th>
                <th className="text-left py-2 px-3 font-medium">Org Role</th>
                <th className="text-left py-2 px-3 font-medium">Auth</th>
                <th className="text-left py-2 px-3 font-medium">Joined</th>
                <th className="text-right py-2 px-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map(u => {
                const isSelf = u.id === user.id
                return (
                  <tr key={u.id} className="border-t" style={{ borderColor: 'var(--border-color)' }}>
                    <td className="py-3 px-3">
                      <div>
                        <div className="font-medium" style={{ color: 'var(--text-primary)' }}>
                          {u.display_name}{isSelf && <span className="text-xs ml-1.5" style={{ color: 'var(--text-muted)' }}>(you)</span>}
                        </div>
                        <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{u.email}</div>
                      </div>
                    </td>
                    <td className="py-3 px-3">
                      <StatusBadge status={u.status} />
                    </td>
                    <td className="py-3 px-3">
                      {!isSelf ? (
                        <select
                          value={u.role}
                          onChange={e => handleRoleChange(u.id, e.target.value)}
                          className="px-2 py-1 rounded text-xs outline-none"
                          style={inputStyle}
                        >
                          <option value="member">member</option>
                          <option value="admin">admin</option>
                          {isOwner && <option value="owner">owner</option>}
                        </select>
                      ) : (
                        <RoleBadge role={u.role} />
                      )}
                    </td>
                    <td className="py-3 px-3">
                      <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--text-muted)' }}>
                        {u.has_oidc ? (
                          <><ShieldCheck size={12} style={{ color: '#22c55e' }} /> SSO</>
                        ) : (
                          <><Shield size={12} /> Local</>
                        )}
                      </span>
                    </td>
                    <td className="py-3 px-3" style={{ color: 'var(--text-muted)' }}>
                      {new Date(u.joined_at).toLocaleDateString()}
                    </td>
                    <td className="py-3 px-3">
                      <div className="flex items-center justify-end gap-1">
                        <button
                          onClick={() => setResetTarget(u)}
                          className="p-1.5 rounded-lg transition-opacity hover:opacity-80"
                          style={{ color: 'var(--brand)' }}
                          title="Reset password"
                        >
                          <KeyRound size={14} />
                        </button>
                        {!isSelf && (
                          <>
                            <button
                              onClick={() => handleToggleStatus(u)}
                              className="p-1.5 rounded-lg transition-opacity hover:opacity-80"
                              style={{ color: u.status === 'active' ? '#f59e0b' : '#22c55e' }}
                              title={u.status === 'active' ? 'Disable user' : 'Enable user'}
                            >
                              {u.status === 'active' ? <Ban size={14} /> : <CheckCircle2 size={14} />}
                            </button>
                            <button
                              onClick={() => setDeleteTarget(u)}
                              className="p-1.5 rounded-lg transition-opacity hover:opacity-80"
                              style={{ color: 'var(--danger)' }}
                              title="Remove from organization"
                            >
                              <Trash2 size={14} />
                            </button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      {/* Reset password modal */}
      {resetTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => { setResetTarget(null); setResetError('') }} />
          <div className="relative w-full max-w-sm mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
            <div className="px-6 py-5" style={gradientBlue}>
              <h2 className="text-lg font-semibold text-white">Reset Password</h2>
              <p className="text-sm text-white/70 mt-0.5">{resetTarget.display_name} ({resetTarget.email})</p>
            </div>
            <form onSubmit={handleResetPassword} className="p-6 space-y-4">
              <InlineError msg={resetError} />
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>New Password</label>
                <input
                  type="password" value={newPassword} onChange={e => setNewPassword(e.target.value)}
                  placeholder="Minimum 8 characters" required minLength={8}
                  className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} autoFocus
                />
              </div>
              <div className="flex gap-3 pt-2">
                <button type="button" onClick={() => { setResetTarget(null); setResetError('') }} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
                <button type="submit" disabled={resetting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={gradientBlue}>
                  {resetting ? <Loader2 size={16} className="animate-spin" /> : 'Reset Password'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Remove from org confirmation modal */}
      {deleteTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => setDeleteTarget(null)} />
          <div className="relative w-full max-w-sm mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
            <div className="px-6 py-5" style={{ background: 'linear-gradient(135deg, #ef4444 0%, #dc2626 100%)' }}>
              <h2 className="text-lg font-semibold text-white">Remove User</h2>
              <p className="text-sm text-white/70 mt-0.5">Remove from {org.name}</p>
            </div>
            <div className="p-6 space-y-4">
              <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                Are you sure you want to remove <strong style={{ color: 'var(--text-primary)' }}>{deleteTarget.display_name}</strong> ({deleteTarget.email}) from this organization? They will lose access to all teams and data in {org.name}.
              </p>
              <div className="flex gap-3 pt-2">
                <button onClick={() => setDeleteTarget(null)} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
                <button onClick={handleDelete} disabled={deleting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={{ background: 'var(--danger)' }}>
                  {deleting ? <Loader2 size={16} className="animate-spin" /> : 'Remove'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Invite user modal */}
      {showInvite && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => { setShowInvite(false); setInviteError('') }} />
          <div className="relative w-full max-w-sm mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
            <div className="px-6 py-5" style={gradientBlue}>
              <h2 className="text-lg font-semibold text-white">Add User</h2>
              <p className="text-sm text-white/70 mt-0.5">Add a user to {org.name}</p>
            </div>
            <form onSubmit={handleInvite} className="p-6 space-y-4">
              <InlineError msg={inviteError} />
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Email</label>
                <input
                  type="email" value={inviteEmail} onChange={e => setInviteEmail(e.target.value)}
                  placeholder="user@company.com" required
                  className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} autoFocus
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Display Name</label>
                <input
                  type="text" value={inviteDisplayName} onChange={e => setInviteDisplayName(e.target.value)}
                  placeholder="Jane Smith" required
                  className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle}
                />
                <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Required if the user is new to the platform</p>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Role</label>
                <select
                  value={inviteRole} onChange={e => setInviteRole(e.target.value)}
                  className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle}
                >
                  <option value="member">Member</option>
                  <option value="admin">Admin</option>
                  {isOwner && <option value="owner">Owner</option>}
                </select>
              </div>
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox" checked={inviteSendEmail} onChange={e => setInviteSendEmail(e.target.checked)}
                  className="w-4 h-4 rounded"
                />
                <span className="text-sm" style={{ color: 'var(--text-secondary)' }}>Send welcome email</span>
              </label>
              <div className="flex gap-3 pt-2">
                <button type="button" onClick={() => { setShowInvite(false); setInviteError('') }} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
                <button type="submit" disabled={inviting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={gradientBlue}>
                  {inviting ? <Loader2 size={16} className="animate-spin" /> : 'Add User'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
