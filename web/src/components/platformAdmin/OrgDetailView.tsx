import { useState, useEffect, useCallback, type FormEvent, type ChangeEvent } from 'react'
import { ArrowLeft, Plus, Trash2, Loader2, Users, ChevronDown, ChevronRight } from 'lucide-react'
import * as adminApi from '../../api/platformAdmin'
import type { AdminOrgDetail, AdminTeam, AdminUserWithRole, TeamMemberDetail } from '../../api/platformAdmin'
import { InlineError, InlineSuccess, StatusBadge, RoleBadge } from './shared'
import { gradientAmber, inputStyle } from './sharedStyles'

// ---------------------------------------------------------------------------
// Org Detail View — Platform Admin
// ---------------------------------------------------------------------------

interface OrgDetailViewProps {
  orgSlug: string
  onBack: () => void
}

export default function OrgDetailView({ orgSlug, onBack }: OrgDetailViewProps) {
  const [orgDetail, setOrgDetail] = useState<AdminOrgDetail | null>(null)
  const [loading, setLoading] = useState<boolean>(true)
  const [error, setError] = useState<string>('')
  const [success, setSuccess] = useState<string>('')
  const [showCreateTeam, setShowCreateTeam] = useState<boolean>(false)
  const [expandedTeam, setExpandedTeam] = useState<string | null>(null)
  const [deleteTeamConfirm, setDeleteTeamConfirm] = useState<{ slug: string; name: string } | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const data = await adminApi.getOrg(orgSlug)
      setOrgDetail(data)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [orgSlug])

  useEffect(() => { void load() }, [load])

  // Auto-dismiss
  useEffect(() => {
    if (success) { const t = setTimeout(() => setSuccess(''), 3000); return () => clearTimeout(t) }
  }, [success])
  useEffect(() => {
    if (error) { const t = setTimeout(() => setError(''), 5000); return () => clearTimeout(t) }
  }, [error])

  const handleDeleteTeam = async (teamSlug: string, teamName: string) => {
    setDeleteTeamConfirm({ slug: teamSlug, name: teamName })
  }

  const confirmDeleteTeam = async () => {
    if (!deleteTeamConfirm) return
    const { slug } = deleteTeamConfirm
    setDeleteTeamConfirm(null)
    try {
      await adminApi.deleteOrgTeam(orgSlug, slug)
      setSuccess(`Team "${slug}" deleted`)
      if (expandedTeam === slug) setExpandedTeam(null)
      load()
    } catch (e) { setError((e as Error).message) }
  }

  if (loading && !orgDetail) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
      </div>
    )
  }

  if (!orgDetail) {
    return (
      <div className="px-6 py-4">
        <InlineError msg={error || 'Organization not found'} />
        <button onClick={onBack} className="mt-4 flex items-center gap-2 text-sm" style={{ color: 'var(--brand)' }}>
          <ArrowLeft size={14} /> Back to Organizations
        </button>
      </div>
    )
  }

  const { organization: org, members: rawMembers, teams: rawTeams } = orgDetail
  const orgMembers = rawMembers ?? []
  const teams = rawTeams ?? []

  return (
    <>
      {/* Status messages */}
      {(error || success) && (
        <div className="px-6 pt-3">
          {error && <InlineError msg={error} />}
          {success && <InlineSuccess msg={success} />}
        </div>
      )}

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {/* Header with back button */}
        <div className="flex items-center gap-4 mb-6">
          <button
            onClick={onBack}
            className="p-2 rounded-lg transition-colors hover:bg-[var(--bg-tertiary)]"
            style={{ color: 'var(--text-secondary)' }}
            title="Back to Organizations"
          >
            <ArrowLeft size={18} />
          </button>
          <div className="flex-1">
            <div className="flex items-center gap-3">
              <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>{org.name}</h2>
              <StatusBadge status={org.status} />
            </div>
            <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>
              {org.slug} &middot; Created {new Date(org.created_at).toLocaleDateString()}
            </p>
          </div>
        </div>

        {/* Teams Section */}
        <div className="mb-8">
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-sm font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
              Teams ({teams.length})
            </h3>
            <button
              onClick={() => setShowCreateTeam(true)}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-white hover:opacity-90"
              style={gradientAmber}
            >
              <Plus size={12} /> New Team
            </button>
          </div>

          <div className="space-y-1">
            {teams.map(team => (
              <TeamRow
                key={team.id}
                team={team}
                orgSlug={orgSlug}
                isExpanded={expandedTeam === team.slug}
                isLastTeam={teams.length <= 1}
                onToggle={() => setExpandedTeam(expandedTeam === team.slug ? null : team.slug)}
                onDelete={() => handleDeleteTeam(team.slug, team.name)}
                onError={setError}
                onSuccess={setSuccess}
              />
            ))}
            {teams.length === 0 && (
              <p className="text-sm py-4 text-center" style={{ color: 'var(--text-muted)' }}>No teams found.</p>
            )}
          </div>
        </div>

        {/* Org Members Section */}
        <div>
          <h3 className="text-sm font-semibold uppercase tracking-wider mb-3" style={{ color: 'var(--text-muted)' }}>
            Organization Members ({orgMembers.length})
          </h3>
          {orgMembers.length === 0 ? (
            <p className="text-sm py-4 text-center" style={{ color: 'var(--text-muted)' }}>No members yet.</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr style={{ color: 'var(--text-muted)' }}>
                  <th className="text-left py-2 px-3 font-medium">User</th>
                  <th className="text-left py-2 px-3 font-medium">Org Role</th>
                </tr>
              </thead>
              <tbody>
                {orgMembers.map(m => (
                  <tr key={m.id} className="border-t" style={{ borderColor: 'var(--border-color)' }}>
                    <td className="py-2.5 px-3">
                      <div className="font-medium" style={{ color: 'var(--text-primary)' }}>{m.display_name}</div>
                      <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{m.email}</div>
                    </td>
                    <td className="py-2.5 px-3"><RoleBadge role={m.role} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Create Team Modal */}
      {showCreateTeam && (
        <CreateTeamModal
          orgSlug={orgSlug}
          onCreated={() => { setShowCreateTeam(false); load() }}
          onCancel={() => setShowCreateTeam(false)}
          onError={setError}
          onSuccess={setSuccess}
        />
      )}

      {/* Delete Team Confirmation */}
      {deleteTeamConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h3 className="text-lg font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Delete Team</h3>
            <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
              Are you sure you want to delete <strong style={{ color: 'var(--text-primary)' }}>{deleteTeamConfirm.name}</strong>? All members will be removed and this cannot be undone.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setDeleteTeamConfirm(null)}
                className="px-4 py-2 rounded-lg text-sm font-medium"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
              >
                Cancel
              </button>
              <button
                onClick={confirmDeleteTeam}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all"
                style={{ background: '#ef4444' }}
              >
                <Trash2 size={14} /> Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// Team Row (expandable with member management)
// ---------------------------------------------------------------------------

interface TeamRowProps {
  team: AdminTeam
  orgSlug: string
  isExpanded: boolean
  isLastTeam: boolean
  onToggle: () => void
  onDelete: () => void
  onError: (msg: string) => void
  onSuccess: (msg: string) => void
}

function TeamRow({ team, orgSlug, isExpanded, isLastTeam, onToggle, onDelete, onError, onSuccess }: TeamRowProps) {
  const [members, setMembers] = useState<TeamMemberDetail[]>([])
  const [loadingMembers, setLoadingMembers] = useState<boolean>(false)
  const [showAddMember, setShowAddMember] = useState<boolean>(false)
  const [removeMemberConfirm, setRemoveMemberConfirm] = useState<{ userId: string; email: string } | null>(null)

  const loadMembers = useCallback(async () => {
    setLoadingMembers(true)
    try {
      const data = await adminApi.listOrgTeamMembers(orgSlug, team.slug)
      setMembers(data ?? [])
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setLoadingMembers(false)
    }
  }, [orgSlug, team.slug, onError])

  useEffect(() => {
    if (isExpanded) { void loadMembers() }
  }, [isExpanded, loadMembers])

  const handleRemoveMember = async (userId: string, email: string) => {
    setRemoveMemberConfirm({ userId, email })
  }

  const confirmRemoveMember = async () => {
    if (!removeMemberConfirm) return
    const { userId, email } = removeMemberConfirm
    setRemoveMemberConfirm(null)
    try {
      await adminApi.removeOrgTeamMember(orgSlug, team.slug, userId)
      onSuccess(`Removed "${email}" from team`)
      loadMembers()
    } catch (e) { onError((e as Error).message) }
  }

  const handleRoleChange = async (userId: string, newRole: string) => {
    try {
      await adminApi.setOrgTeamMemberRole(orgSlug, team.slug, userId, newRole)
      onSuccess(`Role updated`)
      loadMembers()
    } catch (e) { onError((e as Error).message) }
  }

  return (
    <div className="rounded-xl overflow-hidden" style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}>
      {/* Team header row */}
      <div
        className="flex items-center gap-3 px-4 py-3 cursor-pointer transition-colors hover:bg-[var(--bg-tertiary)]"
        onClick={onToggle}
      >
        <span style={{ color: 'var(--text-muted)' }}>
          {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </span>
        <div className="flex-1">
          <span className="font-medium text-sm" style={{ color: 'var(--text-primary)' }}>{team.name}</span>
        </div>
        <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--text-muted)' }}>
          <Users size={12} /> {team.slug}
        </span>
        {!isLastTeam && (
          <button
            onClick={(e) => { e.stopPropagation(); onDelete() }}
            className="p-1.5 rounded-lg transition-opacity hover:opacity-80"
            style={{ color: 'var(--danger)' }}
            title="Delete Team"
          >
            <Trash2 size={14} />
          </button>
        )}
      </div>

      {/* Expanded: team members */}
      {isExpanded && (
        <div className="px-4 pb-4 pt-1" style={{ borderTop: '1px solid var(--border-color)' }}>
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Members</span>
            <button
              onClick={() => setShowAddMember(true)}
              className="flex items-center gap-1 px-2 py-1 rounded-lg text-xs font-medium transition-colors hover:opacity-80"
              style={{ background: 'var(--accent-soft)', color: 'var(--brand)' }}
            >
              <Plus size={11} /> Add Member
            </button>
          </div>

          {loadingMembers ? (
            <div className="flex items-center justify-center py-4">
              <Loader2 size={16} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
            </div>
          ) : members.length === 0 ? (
            <p className="text-xs py-3 text-center" style={{ color: 'var(--text-muted)' }}>No members in this team.</p>
          ) : (
            <table className="w-full text-xs">
              <thead>
                <tr style={{ color: 'var(--text-muted)' }}>
                  <th className="text-left py-1.5 px-2 font-medium">User</th>
                  <th className="text-left py-1.5 px-2 font-medium">Role</th>
                  <th className="text-right py-1.5 px-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                {members.map(m => (
                  <tr key={m.user_id} className="border-t" style={{ borderColor: 'var(--border-color)' }}>
                    <td className="py-2 px-2">
                      <div className="font-medium" style={{ color: 'var(--text-primary)' }}>{m.display_name || m.email}</div>
                      {m.display_name && <div style={{ color: 'var(--text-muted)' }}>{m.email}</div>}
                    </td>
                    <td className="py-2 px-2">
                      <select
                        value={m.role}
                        onChange={(e) => handleRoleChange(m.user_id, e.target.value)}
                        className="px-2 py-1 rounded-lg text-xs outline-none cursor-pointer"
                        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                      >
                        <option value="admin">admin</option>
                        <option value="member">member</option>
                        <option value="viewer">viewer</option>
                      </select>
                    </td>
                    <td className="py-2 px-2 text-right">
                      <button
                        onClick={() => handleRemoveMember(m.user_id, m.email)}
                        className="p-1 rounded transition-opacity hover:opacity-80"
                        style={{ color: 'var(--danger)' }}
                        title="Remove from team"
                      >
                        <Trash2 size={12} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {/* Add Member Modal */}
          {showAddMember && (
            <AddMemberModal
              orgSlug={orgSlug}
              teamSlug={team.slug}
              teamName={team.name}
              onAdded={() => { setShowAddMember(false); loadMembers() }}
              onCancel={() => setShowAddMember(false)}
              onError={onError}
              onSuccess={onSuccess}
            />
          )}

          {/* Remove Member Confirmation */}
          {removeMemberConfirm && (
            <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
              <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
                style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
                <h3 className="text-lg font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Remove Member</h3>
                <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
                  Remove <strong style={{ color: 'var(--text-primary)' }}>{removeMemberConfirm.email}</strong> from team <strong style={{ color: 'var(--text-primary)' }}>{team.name}</strong>?
                </p>
                <div className="flex justify-end gap-3">
                  <button
                    onClick={() => setRemoveMemberConfirm(null)}
                    className="px-4 py-2 rounded-lg text-sm font-medium"
                    style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
                  >
                    Cancel
                  </button>
                  <button
                    onClick={confirmRemoveMember}
                    className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all"
                    style={{ background: '#ef4444' }}
                  >
                    <Trash2 size={14} /> Remove
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Create Team Modal
// ---------------------------------------------------------------------------

interface CreateTeamModalProps {
  orgSlug: string
  onCreated: () => void
  onCancel: () => void
  onError: (msg: string) => void
  onSuccess: (msg: string) => void
}

function CreateTeamModal({ orgSlug, onCreated, onCancel, onError, onSuccess }: CreateTeamModalProps) {
  const [name, setName] = useState<string>('')
  const [slug, setSlug] = useState<string>('')
  const [submitting, setSubmitting] = useState<boolean>(false)
  const [localError, setLocalError] = useState<string>('')

  const handleSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!name.trim()) { setLocalError('Team name is required'); return }
    setSubmitting(true); setLocalError('')
    try {
      await adminApi.createOrgTeam(orgSlug, { name: name.trim(), slug: slug.trim() || undefined })
      onSuccess(`Team "${name.trim()}" created`)
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
          <h2 className="text-lg font-semibold text-white">Create Team</h2>
          <p className="text-sm text-white/70 mt-0.5">Add a new team to &ldquo;{orgSlug}&rdquo;</p>
        </div>
        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          <InlineError msg={localError} />
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Name *</label>
            <input
              type="text"
              value={name}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setName(e.target.value)}
              placeholder="Engineering"
              className="w-full px-4 py-2.5 rounded-xl text-sm outline-none"
              style={inputStyle}
              autoFocus
            />
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>
              Slug <span className="text-xs font-normal" style={{ color: 'var(--text-muted)' }}>(auto-generated if empty)</span>
            </label>
            <input
              type="text"
              value={slug}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setSlug(e.target.value)}
              placeholder="engineering"
              className="w-full px-4 py-2.5 rounded-xl text-sm outline-none"
              style={inputStyle}
            />
          </div>
          <div className="flex gap-3 pt-2">
            <button type="button" onClick={onCancel} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              style={gradientAmber}
            >
              {submitting ? <Loader2 size={16} className="animate-spin" /> : 'Create Team'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Add Member Modal
// ---------------------------------------------------------------------------

interface AddMemberModalProps {
  orgSlug: string
  teamSlug: string
  teamName: string
  onAdded: () => void
  onCancel: () => void
  onError: (msg: string) => void
  onSuccess: (msg: string) => void
}

function AddMemberModal({ orgSlug, teamSlug, teamName, onAdded, onCancel, onError, onSuccess }: AddMemberModalProps) {
  const [email, setEmail] = useState<string>('')
  const [role, setRole] = useState<string>('member')
  const [submitting, setSubmitting] = useState<boolean>(false)
  const [localError, setLocalError] = useState<string>('')

  const handleSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!email.trim()) { setLocalError('Email is required'); return }
    setSubmitting(true); setLocalError('')
    try {
      await adminApi.addOrgTeamMember(orgSlug, teamSlug, { email: email.trim(), role })
      onSuccess(`Added "${email.trim()}" to ${teamName}`)
      onAdded()
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
        <div className="px-6 py-5" style={{ background: 'linear-gradient(135deg, #3b82f6 0%, #2563eb 100%)' }}>
          <h2 className="text-lg font-semibold text-white">Add Member</h2>
          <p className="text-sm text-white/70 mt-0.5">Add a user to team &ldquo;{teamName}&rdquo;</p>
        </div>
        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          <InlineError msg={localError} />
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Email *</label>
            <input
              type="email"
              value={email}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setEmail(e.target.value)}
              placeholder="user@company.com"
              className="w-full px-4 py-2.5 rounded-xl text-sm outline-none"
              style={inputStyle}
              autoFocus
            />
            <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
              The user must already exist on the platform. They will be auto-added to the organization if not already a member.
            </p>
          </div>
          <div>
            <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Role</label>
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              className="w-full px-4 py-2.5 rounded-xl text-sm outline-none cursor-pointer"
              style={inputStyle}
            >
              <option value="member">Member</option>
              <option value="admin">Admin</option>
              <option value="viewer">Viewer</option>
            </select>
          </div>
          <div className="flex gap-3 pt-2">
            <button type="button" onClick={onCancel} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              style={{ background: 'linear-gradient(135deg, #3b82f6 0%, #2563eb 100%)' }}
            >
              {submitting ? <Loader2 size={16} className="animate-spin" /> : 'Add Member'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
