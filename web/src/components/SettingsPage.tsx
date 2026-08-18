import { useState, useEffect, useCallback, lazy, Suspense, type FormEvent } from 'react'
import { ChevronRight, Download, Plus, Trash2, UserPlus, AlertCircle, Loader2, Key } from 'lucide-react'
import SettingsContent from './settings/SettingsContent'
import { PERSONAL_ITEMS, TEAM_ITEMS, ORG_ITEMS, PLATFORM_ITEMS, PLATFORM_SYSTEM_SECTIONS } from './settings/settingsMenuItems'
import type { SettingsMenuItem } from './settings/settingsMenuItems'
import { useSettingsData } from '../hooks/useSettingsData'
import type { UpdateInfo, MCPServerConfig, SettingsData, ProviderInfo } from './settings/settingsApi'
import { fetchMCPConfig, fetchPlatformProviders, fetchOrgProviders, fetchTeamProviders, fetchEffectiveProviders, fetchSettings, savePlatformProviders, saveOrgProviders, saveSettings as saveTeamSettings, fetchStandardServers } from './settings/settingsApi'
import {
  fetchTeams, createTeam, deleteTeam,
  fetchTeamMembers, addTeamMember, removeTeamMember, setTeamMemberRole,
  type Team, type TeamMember,
} from '../api/platform'
import { teamFetch } from '../api/teamContext'
import { fetchA2AAgents } from '../api/a2aAgents'
import type { A2AAgentListItem } from '../api/a2aAgents'

declare const __UI_VERSION__: string

// Lazy-loaded sub-views
const UserManagement = lazy(() => import('./UserManagement'))
const AuditViewer = lazy(() => import('./AuditViewer'))
const SkillsSettings = lazy(() => import('./settings/SkillsSettings'))
const MCPServersSettings = lazy(() => import('./settings/MCPServersSettings'))
const ProvidersSettings = lazy(() => import('./settings/ProvidersSettings'))
const TapsSettings = lazy(() => import('./settings/TapsSettings'))
const FlowStorePanel = lazy(() => import('./FlowStorePanel'))
const TeamContainerTab = lazy(() => import('./TeamContainerTab'))
const PlatformAdminPanel = lazy(() => import('./PlatformAdminPanel'))
const KnowledgeBrowser = lazy(() => import('./KnowledgeBrowser'))
const NetworkPolicySettings = lazy(() => import('./settings/NetworkPolicySettings'))
const A2AAgentsSettings = lazy(() => import('./settings/A2AAgentsSettings'))

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SettingsPageProps {
  activeSection?: string
  onSectionChange?: (section: string) => void
  onToolsRefresh?: () => void
  onSettingsSaved?: () => void
  updateAvailable?: UpdateInfo | null
  onUpdateClick?: (() => void) | null
  appVersion?: string
  theme?: string
  isPlatformMode?: boolean
  userRole?: string
  /** User's platform-level role (e.g. 'superadmin') */
  platformRole?: string
  /** Current user info (for team management) */
  user?: { id: string; email: string; display_name: string; role: string; platform_role?: string } | null
  /** Current org info */
  org?: { id: string; name: string; slug: string } | null
  /** Active team from TopBar context */
  activeTeam?: string | null
  /** All teams available to the user */
  teams?: { slug: string; name: string }[] | null
  /** URL params for team section */
  teamSlug?: string
  teamTab?: string
  /** URL params for org/platform sub-sections */
  subsection?: string
}

interface MenuCategory {
  label?: string
  items: SettingsMenuItem[]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const slugify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
const errMsg = (err: unknown, fallback: string) => err instanceof Error ? err.message : fallback
const gradientPurple = { background: 'var(--brand)' }
const inputStyle = { background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }
const errorBg = { background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)', border: '1px solid rgba(239, 68, 68, 0.2)' }

function InlineError({ msg }: { msg: string }) {
  if (!msg) return null
  return (
    <div className="flex items-center gap-2 p-3 rounded-lg text-sm" style={errorBg}>
      <AlertCircle size={14} /><span>{msg}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// MembersPanel — team member management
// ---------------------------------------------------------------------------

interface MembersPanelProps {
  user: { id: string; email: string; display_name: string; role: string }
  team: Team
  canManageTeam: boolean
}

function MembersPanel({ user, team, canManageTeam }: MembersPanelProps) {
  const [members, setMembers] = useState<TeamMember[]>([])
  const [membersLoading, setMembersLoading] = useState(true)
  const [membersError, setMembersError] = useState('')
  const [showAddMember, setShowAddMember] = useState(false)
  const [addEmail, setAddEmail] = useState('')
  const [addRole, setAddRole] = useState('member')
  const [addNotify, setAddNotify] = useState(true)
  const [adding, setAdding] = useState(false)
  const [addError, setAddError] = useState('')

  const loadMembers = useCallback(async () => {
    setMembersLoading(true); setMembersError('')
    try {
      const resp = await fetchTeamMembers(team.slug)
      setMembers(resp.members)
    }
    catch (err) { setMembersError(errMsg(err, 'Failed to load members')) }
    finally { setMembersLoading(false) }
  }, [team.slug])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      setMembersLoading(true); setMembersError('')
      try {
        const resp = await fetchTeamMembers(team.slug)
        if (cancelled) return
        setMembers(resp.members)
      }
      catch (err) { if (!cancelled) setMembersError(errMsg(err, 'Failed to load members')) }
      finally { if (!cancelled) setMembersLoading(false) }
    }
    load()
    return () => { cancelled = true }
  }, [team.slug])

  const handleAddMember = async (e: FormEvent) => {
    e.preventDefault()
    setAdding(true); setAddError('')
    try {
      await addTeamMember(team.slug, addEmail, addRole, addNotify)
      setShowAddMember(false); setAddEmail(''); setAddRole('member'); setAddNotify(true)
      await loadMembers()
    } catch (err) { setAddError(errMsg(err, 'Failed to add member')) }
    finally { setAdding(false) }
  }

  const handleRemoveMember = async (userId: string) => {
    try { await removeTeamMember(team.slug, userId); await loadMembers() }
    catch (err) { setMembersError(errMsg(err, 'Failed to remove member')) }
  }

  const handleRoleChange = async (userId: string, role: string) => {
    try { await setTeamMemberRole(team.slug, userId, role); await loadMembers() }
    catch (err) { setMembersError(errMsg(err, 'Failed to update role')) }
  }

  return (
    <div className="h-full flex flex-col overflow-hidden">
      {canManageTeam && (
        <div className="flex items-center justify-end px-6 py-3 border-b" style={{ borderColor: 'var(--border-color)' }}>
          <button onClick={() => setShowAddMember(true)} className="flex items-center gap-2 px-4 py-2 rounded-xl text-white text-sm font-medium hover:opacity-90" style={gradientPurple}>
            <UserPlus size={16} />Add Member
          </button>
        </div>
      )}

      {showAddMember && (
        <form onSubmit={handleAddMember} className="flex items-center gap-3 px-6 py-3 border-b flex-wrap" style={{ borderColor: 'var(--border-color)', background: 'var(--bg-secondary)' }}>
          <input type="email" value={addEmail} onChange={e => setAddEmail(e.target.value)} placeholder="Email address" required className="flex-1 px-3 py-2 rounded-lg text-sm outline-none" style={inputStyle} autoFocus />
          <select value={addRole} onChange={e => setAddRole(e.target.value)} className="px-3 py-2 rounded-lg text-sm outline-none" style={inputStyle}>
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
          <label className="flex items-center gap-1.5 text-xs cursor-pointer" style={{ color: 'var(--text-secondary)' }}>
            <input type="checkbox" checked={addNotify} onChange={e => setAddNotify(e.target.checked)} className="rounded" />
            Notify
          </label>
          <button type="submit" disabled={adding} className="px-4 py-2 rounded-lg text-white text-sm font-medium hover:opacity-90 disabled:opacity-50" style={gradientPurple}>
            {adding ? <Loader2 size={16} className="animate-spin" /> : 'Add'}
          </button>
          <button type="button" onClick={() => { setShowAddMember(false); setAddError('') }} className="px-3 py-2 rounded-lg text-sm" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
          {addError && <span className="text-xs flex items-center gap-1" style={{ color: 'var(--danger)' }}><AlertCircle size={12} />{addError}</span>}
        </form>
      )}

      {membersError && <div className="mx-6 mt-3"><InlineError msg={membersError} /></div>}

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {membersLoading ? (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
          </div>
        ) : members.length === 0 ? (
          <p className="text-center py-12 text-sm" style={{ color: 'var(--text-muted)' }}>No members in this team.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr style={{ color: 'var(--text-muted)' }}>
                <th className="text-left py-2 px-3 font-medium">Email</th>
                <th className="text-left py-2 px-3 font-medium">Name</th>
                <th className="text-left py-2 px-3 font-medium">Role</th>
                <th className="text-left py-2 px-3 font-medium">Joined</th>
                {canManageTeam && <th className="w-10" />}
              </tr>
            </thead>
            <tbody>
              {members.map(m => (
                <tr key={m.user_id} className="border-t" style={{ borderColor: 'var(--border-color)' }}>
                  <td className="py-2.5 px-3" style={{ color: 'var(--text-primary)' }}>{m.email}</td>
                  <td className="py-2.5 px-3" style={{ color: 'var(--text-secondary)' }}>{m.display_name}</td>
                  <td className="py-2.5 px-3">
                    {canManageTeam ? (
                      <select value={m.role} onChange={e => handleRoleChange(m.user_id, e.target.value)} className="px-2 py-1 rounded text-xs outline-none" style={inputStyle}>
                        <option value="viewer">viewer</option>
                        <option value="member">member</option>
                        <option value="admin">admin</option>
                      </select>
                    ) : (
                      <span className="px-2 py-0.5 rounded text-xs" style={{ background: 'var(--accent-soft)', color: 'var(--brand)' }}>{m.role}</span>
                    )}
                  </td>
                  <td className="py-2.5 px-3" style={{ color: 'var(--text-muted)' }}>{new Date(m.joined_at).toLocaleDateString()}</td>
                  {canManageTeam && (
                    <td className="py-2.5 px-3">
                      {m.user_id !== user.id && (
                        <button onClick={() => handleRemoveMember(m.user_id)} className="p-1 rounded hover:opacity-80 transition-opacity" style={{ color: 'var(--danger)' }} title="Remove member">
                          <Trash2 size={14} />
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamMCPServersTab — wraps MCPServersSettings with team-scoped data
// ---------------------------------------------------------------------------

function TeamMCPServersTab({ teamSlug, theme }: { teamSlug: string; theme: string }) {
  const [mcpServers, setMcpServers] = useState<Record<string, MCPServerConfig>>({})
  const [mcpServerNames, setMcpServerNames] = useState<Record<string, string>>({})
  const [mcpServerArgs, setMcpServerArgs] = useState<Record<string, string>>({})
  const [, setMcpHasChanges] = useState(false)
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [, setError] = useState<string | null>(null)

  // Org-level MCP servers (inherited, read-only)
  const [orgServers, setOrgServers] = useState<Record<string, MCPServerConfig>>({})
  // Platform-level MCP servers (inherited, read-only)
  const [platformServers, setPlatformServers] = useState<Record<string, MCPServerConfig>>({})

  const loadData = useCallback(async () => {
    try {
      const data = await fetchMCPConfig(teamSlug)
      const servers = data.mcpServers || {}
      setMcpServers(servers)
      const names: Record<string, string> = {}
      const args: Record<string, string> = {}
      Object.entries(servers).forEach(([name, server]) => {
        names[name] = name
        args[name] = (server.args || []).join(', ')
      })
      setMcpServerNames(names)
      setMcpServerArgs(args)
    } catch (err) {
      console.error('Failed to load team MCP config:', err)
    }
  }, [teamSlug])

  const loadOrgServers = useCallback(async () => {
    try {
      // Fetch org-level MCP config (no scope=team)
      const data = await fetchMCPConfig()
      setOrgServers(data.mcpServers || {})
    } catch {
      // Org servers are optional/inherited — ignore errors
    }
  }, [])

  const loadPlatformServers = useCallback(async () => {
    try {
      const data = await fetchMCPConfig(undefined, 'platform')
      setPlatformServers(data.mcpServers || {})
    } catch {
      // Platform servers are optional/inherited — ignore errors
    }
  }, [])

  useEffect(() => { loadData(); loadOrgServers(); loadPlatformServers() }, [loadData, loadOrgServers, loadPlatformServers])

  const platformServerEntries = Object.entries(platformServers)
  const orgServerEntries = Object.entries(orgServers)

  return (
    <div className="space-y-6">
      {/* Platform-level inherited MCP servers (read-only) */}
      {platformServerEntries.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <div className="space-y-2">
            {platformServerEntries.map(([name, server]) => (
              <div
                key={name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: server.enabled !== false ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>
                      {server.transport === 'sse' || server.url ? server.url || 'SSE' : `${server.command || ''} ${(server.args || []).join(' ')}`.trim() || 'No command'}
                    </div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                  platform
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Org-level inherited MCP servers (read-only) */}
      {orgServerEntries.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Organization (inherited)
          </div>
          <div className="space-y-2">
            {orgServerEntries.map(([name, server]) => (
              <div
                key={name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: server.enabled !== false ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>
                      {server.transport === 'sse' || server.url ? server.url || 'SSE' : `${server.command || ''} ${(server.args || []).join(' ')}`.trim() || 'No command'}
                    </div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'rgba(59, 130, 246, 0.15)', color: '#3b82f6' }}>
                  org
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Team-level MCP servers (editable) */}
      {(orgServerEntries.length > 0 || platformServerEntries.length > 0) && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Team
        </div>
      )}
      <MCPServersSettings
        mcpServers={mcpServers}
        setMcpServers={setMcpServers}
        mcpServerNames={mcpServerNames}
        setMcpServerNames={setMcpServerNames}
        mcpServerArgs={mcpServerArgs}
        setMcpServerArgs={setMcpServerArgs}
        setMcpHasChanges={setMcpHasChanges}
        standardServers={[]}
        saving={saving}
        setSaving={setSaving}
        setSaveSuccess={setSaveSuccess}
        setError={setError}
        onToolsRefresh={loadData}
        loadData={loadData}
        setGeneralForm={() => {}}
        theme={theme}
        teamSlug={teamSlug}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamA2AAgentsTab — wraps A2AAgentsSettings with team-scoped data + inherited
// ---------------------------------------------------------------------------

function TeamA2AAgentsTab({ teamSlug, theme }: { teamSlug: string; theme: string }) {
  const [orgAgents, setOrgAgents] = useState<A2AAgentListItem[]>([])
  const [platformAgents, setPlatformAgents] = useState<A2AAgentListItem[]>([])

  const loadInherited = useCallback(async () => {
    try {
      const orgData = await fetchA2AAgents('org')
      setOrgAgents(orgData.agents || [])
    } catch {
      // Org agents are optional/inherited — ignore errors
    }
    try {
      const platformData = await fetchA2AAgents('platform')
      setPlatformAgents(platformData.agents || [])
    } catch {
      // Platform agents are optional/inherited — ignore errors
    }
  }, [])

  useEffect(() => { loadInherited() }, [loadInherited])

  return (
    <div className="space-y-6">
      {/* Platform-level inherited A2A agents (read-only) */}
      {platformAgents.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <div className="space-y-2">
            {platformAgents.map(agent => (
              <div
                key={agent.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: agent.enabled ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{agent.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{agent.url}</div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                  platform
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Org-level inherited A2A agents (read-only) */}
      {orgAgents.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Organization (inherited)
          </div>
          <div className="space-y-2">
            {orgAgents.map(agent => (
              <div
                key={agent.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: agent.enabled ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{agent.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{agent.url}</div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'rgba(59, 130, 246, 0.15)', color: '#3b82f6' }}>
                  org
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Team-level A2A agents (editable) */}
      {(orgAgents.length > 0 || platformAgents.length > 0) && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Team
        </div>
      )}
      <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
        <A2AAgentsSettings scope="team" teamSlug={teamSlug} theme={theme} onRefresh={loadInherited} />
      </Suspense>
    </div>
  )
}

// ---------------------------------------------------------------------------
// OrgA2AAgentsTab — wraps A2AAgentsSettings with org-scoped data
// ---------------------------------------------------------------------------

function OrgA2AAgentsTab({ theme }: { theme: string }) {
  const [platformAgents, setPlatformAgents] = useState<A2AAgentListItem[]>([])

  const loadPlatformAgents = useCallback(async () => {
    try {
      const data = await fetchA2AAgents('platform')
      setPlatformAgents(data.agents || [])
    } catch {
      // Platform agents are optional/inherited — ignore errors
    }
  }, [])

  useEffect(() => { loadPlatformAgents() }, [loadPlatformAgents])

  return (
    <div className="space-y-6">
      {/* Platform-level inherited A2A agents (read-only) */}
      {platformAgents.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <div className="space-y-2">
            {platformAgents.map(agent => (
              <div
                key={agent.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: agent.enabled ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{agent.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{agent.url}</div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                  platform
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Org-level A2A agents (editable) */}
      {platformAgents.length > 0 && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Organization
        </div>
      )}
      <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
        <A2AAgentsSettings scope="org" theme={theme} />
      </Suspense>
    </div>
  )
}

// ---------------------------------------------------------------------------
// PlatformA2AAgentsTab — A2AAgentsSettings for platform-level management (superadmin)
// ---------------------------------------------------------------------------

function PlatformA2AAgentsTab({ theme }: { theme: string }) {
  return (
    <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
      <A2AAgentsSettings scope="platform" theme={theme} />
    </Suspense>
  )
}

// ---------------------------------------------------------------------------
// OrgMCPServersTab — wraps MCPServersSettings with org-scoped data
// ---------------------------------------------------------------------------

function OrgMCPServersTab({ theme }: { theme: string }) {
  const [mcpServers, setMcpServers] = useState<Record<string, MCPServerConfig>>({})
  const [mcpServerNames, setMcpServerNames] = useState<Record<string, string>>({})
  const [mcpServerArgs, setMcpServerArgs] = useState<Record<string, string>>({})
  const [, setMcpHasChanges] = useState(false)
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [, setError] = useState<string | null>(null)

  // Platform-level MCP servers (inherited, read-only)
  const [platformServers, setPlatformServers] = useState<Record<string, MCPServerConfig>>({})

  const loadData = useCallback(async () => {
    try {
      // No teamSlug → fetches org-level MCP config
      const data = await fetchMCPConfig()
      const servers = data.mcpServers || {}
      setMcpServers(servers)
      const names: Record<string, string> = {}
      const args: Record<string, string> = {}
      Object.entries(servers).forEach(([name, server]) => {
        names[name] = name
        args[name] = (server.args || []).join(', ')
      })
      setMcpServerNames(names)
      setMcpServerArgs(args)
    } catch (err) {
      console.error('Failed to load org MCP config:', err)
    }
  }, [])

  const loadPlatformServers = useCallback(async () => {
    try {
      const data = await fetchMCPConfig(undefined, 'platform')
      setPlatformServers(data.mcpServers || {})
    } catch {
      // Platform servers are optional/inherited — ignore errors
    }
  }, [])

  useEffect(() => { loadData(); loadPlatformServers() }, [loadData, loadPlatformServers])

  const platformServerEntries = Object.entries(platformServers)

  return (
    <div className="space-y-6">
      {/* Platform-level inherited MCP servers (read-only) */}
      {platformServerEntries.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <div className="space-y-2">
            {platformServerEntries.map(([name, server]) => (
              <div
                key={name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ background: server.enabled !== false ? '#22c55e' : '#6b7280' }}
                  />
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>
                      {server.transport === 'sse' || server.url ? server.url || 'SSE' : `${server.command || ''} ${(server.args || []).join(' ')}`.trim() || 'No command'}
                    </div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full shrink-0" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                  platform
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Org-level MCP servers (editable) */}
      {platformServerEntries.length > 0 && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Organization
        </div>
      )}
      <MCPServersSettings
        mcpServers={mcpServers}
        setMcpServers={setMcpServers}
        mcpServerNames={mcpServerNames}
        setMcpServerNames={setMcpServerNames}
        mcpServerArgs={mcpServerArgs}
        setMcpServerArgs={setMcpServerArgs}
        setMcpHasChanges={setMcpHasChanges}
        standardServers={[]}
        saving={saving}
        setSaving={setSaving}
        setSaveSuccess={setSaveSuccess}
        setError={setError}
        onToolsRefresh={loadData}
        loadData={loadData}
        setGeneralForm={() => {}}
        theme={theme}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// PlatformMCPServersTab — wraps MCPServersSettings for platform-level MCP management (superadmin)
// ---------------------------------------------------------------------------

function PlatformMCPServersTab({ theme }: { theme: string }) {
  const [mcpServers, setMcpServers] = useState<Record<string, MCPServerConfig>>({})
  const [mcpServerNames, setMcpServerNames] = useState<Record<string, string>>({})
  const [mcpServerArgs, setMcpServerArgs] = useState<Record<string, string>>({})
  const [, setMcpHasChanges] = useState(false)
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [, setError] = useState<string | null>(null)
  const [standardServers, setStandardServers] = useState<any[]>([])

  const loadData = useCallback(async () => {
    try {
      const [data, stdServers] = await Promise.all([
        fetchMCPConfig(undefined, 'platform'),
        fetchStandardServers('platform').catch(() => ({ servers: [] as any[] }))
      ])
      const servers = data.mcpServers || {}
      setMcpServers(servers)
      setStandardServers(stdServers.servers || [])
      const names: Record<string, string> = {}
      const args: Record<string, string> = {}
      Object.entries(servers).forEach(([name, server]) => {
        names[name] = name
        args[name] = (server.args || []).join(', ')
      })
      setMcpServerNames(names)
      setMcpServerArgs(args)
    } catch (err) {
      console.error('Failed to load platform MCP config:', err)
    }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  return (
    <MCPServersSettings
      mcpServers={mcpServers}
      setMcpServers={setMcpServers}
      mcpServerNames={mcpServerNames}
      setMcpServerNames={setMcpServerNames}
      mcpServerArgs={mcpServerArgs}
      setMcpServerArgs={setMcpServerArgs}
      setMcpHasChanges={setMcpHasChanges}
      standardServers={standardServers}
      saving={saving}
      setSaving={setSaving}
      setSaveSuccess={setSaveSuccess}
      setError={setError}
      onToolsRefresh={loadData}
      loadData={loadData}
      setGeneralForm={() => {}}
      theme={theme}
      scope="platform"
    />
  )
}

// ---------------------------------------------------------------------------
// TeamNetworkPolicyTab — shows inherited (platform + org) + editable team rules
// ---------------------------------------------------------------------------

function TeamNetworkPolicyTab({ teamSlug }: { teamSlug: string }) {
  const [orgRules, setOrgRules] = useState<any[]>([])
  const [platformRules, setPlatformRules] = useState<any[]>([])
  const [refreshKey, setRefreshKey] = useState(0)

  const loadInherited = useCallback(async () => {
    try {
      const [orgRes, platformRes] = await Promise.all([
        teamFetch('/api/network-policies?scope=org', undefined, teamSlug),
        teamFetch('/api/network-policies?scope=platform'),
      ])
      if (orgRes.ok) {
        const data = await orgRes.json()
        setOrgRules(data.rules || [])
      }
      if (platformRes.ok) {
        const data = await platformRes.json()
        setPlatformRules(data.rules || [])
      }
    } catch {
      // Inherited rules are optional
    }
  }, [teamSlug])

  useEffect(() => { loadInherited() }, [loadInherited])

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-lg font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>Network Policy</h3>
        <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
          Control which hosts sandbox containers can access. Deny rules from higher tiers cannot be overridden.
        </p>
      </div>

      {platformRules.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <NetworkPolicySettings scope="platform" readOnly rules={platformRules} />
        </div>
      )}

      {orgRules.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Organization (inherited)
          </div>
          <NetworkPolicySettings scope="org" readOnly rules={orgRules} />
        </div>
      )}

      {(platformRules.length > 0 || orgRules.length > 0) && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Team
        </div>
      )}
      <NetworkPolicySettings key={refreshKey} scope="team" teamSlug={teamSlug} onRulesChange={() => setRefreshKey(k => k + 1)} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// OrgNetworkPolicyTab — shows inherited platform + editable org rules
// ---------------------------------------------------------------------------

function OrgNetworkPolicyTab() {
  const [platformRules, setPlatformRules] = useState<any[]>([])
  const [refreshKey, setRefreshKey] = useState(0)

  const loadInherited = useCallback(async () => {
    try {
      const res = await teamFetch('/api/network-policies?scope=platform')
      if (res.ok) {
        const data = await res.json()
        setPlatformRules(data.rules || [])
      }
    } catch {
      // Platform rules are optional
    }
  }, [])

  useEffect(() => { loadInherited() }, [loadInherited])

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-lg font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>Network Policy</h3>
        <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
          Organization-level network rules. These apply to all teams and cannot be overridden by team-level allow rules.
        </p>
      </div>

      {platformRules.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <NetworkPolicySettings scope="platform" readOnly rules={platformRules} />
        </div>
      )}

      {platformRules.length > 0 && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Organization
        </div>
      )}
      <NetworkPolicySettings key={refreshKey} scope="org" onRulesChange={() => setRefreshKey(k => k + 1)} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// PlatformNetworkPolicyTab — platform-wide rules (superadmin only)
// ---------------------------------------------------------------------------

function PlatformNetworkPolicyTab() {
  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-lg font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>Network Policy</h3>
        <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
          Platform-wide network rules. Deny rules here cannot be overridden by any org or team. Allow rules apply to all sandboxes.
        </p>
      </div>
      <NetworkPolicySettings scope="platform" />
    </div>
  )
}

// ---------------------------------------------------------------------------
// PlatformProvidersTab — wraps ProvidersSettings for platform-level provider management (superadmin)
// ---------------------------------------------------------------------------

function PlatformProvidersTab() {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [providerForms, setProviderForms] = useState<Record<string, Record<string, string>>>({})
  const [generalForm, setGeneralForm] = useState({ default_provider: '', default_model: '' })
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Providers configured in config.yaml (usable at runtime, but managed by
  // editing config.yaml so read-only here). Surfaced so operators can see
  // defaults baked into the deployment (e.g. Kubernetes).
  const [configFileProviders, setConfigFileProviders] = useState<{ name: string; type: string; level: string }[]>([])

  const loadData = useCallback(async () => {
    try {
      const data = await fetchPlatformProviders()
      const providers: ProviderInfo[] = []
      if (data.providers) {
        for (const [name, fields] of Object.entries(data.providers)) {
          const type = fields?.type || name
          const fieldsCopy = { ...fields }
          delete fieldsCopy.type
          providers.push({
            name,
            type,
            display_name: type,
            configured: Object.keys(fieldsCopy).length > 0,
            fields: fieldsCopy
          })
        }
      }
      const settingsData: SettingsData = {
        general: {
          default_provider: data.default_provider || '',
          default_model: data.default_model || '',
          web_search_tool: '',
          web_extract_tool: '',
          timezone: ''
        },
        providers
      }
      setSettings(settingsData)
      setGeneralForm({ default_provider: data.default_provider || '', default_model: data.default_model || '' })
      const pForms: Record<string, Record<string, string>> = {}
      providers.forEach(p => { pForms[p.name] = { ...p.fields } })
      setProviderForms(pForms)
    } catch (err) {
      console.error('Failed to load platform providers:', err)
    }

    // Surface config.yaml providers (source === 'local') as read-only entries.
    try {
      const eff = await fetchEffectiveProviders()
      const local: { name: string; type: string; level: string }[] = []
      if (eff.providers && eff.provider_sources) {
        for (const [name, src] of Object.entries(eff.provider_sources)) {
          if (src?.source === 'local') {
            const type = eff.providers[name]?.type || name
            local.push({ name, type, level: 'config.yaml' })
          }
        }
      }
      setConfigFileProviders(local)
    } catch { /* effective endpoint optional; ignore */ }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  return (
    <div className="space-y-6">
      {configFileProviders.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            From config.yaml (read-only)
          </div>
          <div className="space-y-2">
            {configFileProviders.map(p => (
              <div
                key={p.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
                    style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                    <Key size={14} style={{ color: 'var(--brand)' }} />
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{p.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{p.type}</div>
                  </div>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded-full"
                  style={{ background: 'rgba(148, 163, 184, 0.15)', color: '#94a3b8' }}>
                  config.yaml
                </span>
              </div>
            ))}
          </div>
          <p className="text-xs mt-2" style={{ color: 'var(--text-muted)' }}>
            Configured in the deployment&apos;s config.yaml. Shown once regardless of pod count. Managed by editing config.yaml, not here.
          </p>
        </div>
      )}

      <ProvidersSettings
        settings={settings}
        providerForms={providerForms}
        setProviderForms={setProviderForms}
        generalForm={generalForm}
        setGeneralForm={setGeneralForm}
        saving={saving}
        setSaving={setSaving}
        setSaveSuccess={setSaveSuccess}
        error={error}
        setError={setError}
        loadData={loadData}
        level="platform"
        inheritedProviders={configFileProviders.map(p => ({ name: p.name, type: p.type, level: p.level }))}
        onSaveDefault={async (provider, model) => {
          await savePlatformProviders({ default_provider: provider, default_model: model })
          loadData()
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// OrgProvidersTab — wraps ProvidersSettings with org-scoped data + inherited platform providers
// ---------------------------------------------------------------------------

function OrgProvidersTab() {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [providerForms, setProviderForms] = useState<Record<string, Record<string, string>>>({})
  const [generalForm, setGeneralForm] = useState({ default_provider: '', default_model: '' })
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Inherited providers (read-only)
  const [platformProviders, setPlatformProviders] = useState<{ name: string; type: string; configured: boolean }[]>([])
  // config.yaml providers (read-only, but selectable as default at any level)
  const [configFileProviders, setConfigFileProviders] = useState<{ name: string; type: string; level: string }[]>([])
  // Platform-level defaults for inheritance display
  const [platformDefaults, setPlatformDefaults] = useState<{ provider: string; model: string }>({ provider: '', model: '' })

  const loadData = useCallback(async () => {
    // Load org-level settings (raw, no cascade)
    try {
      const data = await fetchOrgProviders()
      const providers: ProviderInfo[] = []
      if (data.providers) {
        for (const [name, fields] of Object.entries(data.providers)) {
          const type = fields?.type || name
          const fieldsCopy = { ...fields }
          delete fieldsCopy.type
          providers.push({
            name,
            type,
            display_name: type,
            configured: Object.keys(fieldsCopy).length > 0,
            fields: fieldsCopy
          })
        }
      }
      const settingsData: SettingsData = {
        general: {
          default_provider: data.default_provider || '',
          default_model: data.default_model || '',
          web_search_tool: '',
          web_extract_tool: '',
          timezone: ''
        },
        providers
      }
      setSettings(settingsData)
      // Show only the org's explicit values (empty = "Not Set")
      setGeneralForm({
        default_provider: data.default_provider || '',
        default_model: data.default_model || ''
      })
      const pForms: Record<string, Record<string, string>> = {}
      providers.forEach(p => { pForms[p.name] = { ...p.fields } })
      setProviderForms(pForms)
    } catch (err) {
      console.error('Failed to load org providers:', err)
    }

    // Load platform providers (for inherited list + defaults display)
    try {
      const platformData = await fetchPlatformProviders()
      const items: { name: string; type: string; configured: boolean }[] = []
      if (platformData.providers) {
        for (const [name, fields] of Object.entries(platformData.providers)) {
          const type = fields?.type || name
          const fieldsCopy = { ...fields }
          delete fieldsCopy.type
          items.push({ name, type, configured: Object.keys(fieldsCopy).length > 0 })
        }
      }
      setPlatformProviders(items)
      setPlatformDefaults({
        provider: platformData.default_provider || '',
        model: platformData.default_model || ''
      })
    } catch {
      // Platform providers are optional
    }

    // Surface config.yaml providers (source === 'local') from the effective
    // endpoint so they can be shown read-only AND selected as a default here.
    try {
      const eff = await fetchEffectiveProviders()
      const local: { name: string; type: string; level: string }[] = []
      if (eff.providers && eff.provider_sources) {
        for (const [name, src] of Object.entries(eff.provider_sources)) {
          if (src?.source === 'local') {
            const type = eff.providers[name]?.type || name
            local.push({ name, type, level: 'config.yaml' })
          }
        }
      }
      setConfigFileProviders(local)
    } catch { /* effective endpoint optional; ignore */ }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  return (
    <div className="space-y-6">
      {/* Inherited platform providers (read-only) */}
      {platformProviders.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Platform (inherited)
          </div>
          <div className="space-y-2">
            {platformProviders.map(p => (
              <div
                key={p.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
                    style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                    <Key size={14} style={{ color: 'var(--brand)' }} />
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{p.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{p.type}</div>
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {generalForm.default_provider === p.name && (
                    <span className="text-[10px] px-2 py-0.5 rounded-full"
                      style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                      default
                    </span>
                  )}
                  <span className="text-[10px] px-2 py-0.5 rounded-full"
                    style={{ background: 'rgba(59, 130, 246, 0.15)', color: '#3b82f6' }}>
                    platform
                  </span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* config.yaml providers (read-only; selectable as default) */}
      {configFileProviders.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            From config.yaml (read-only)
          </div>
          <div className="space-y-2">
            {configFileProviders.map(p => (
              <div
                key={p.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
                    style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                    <Key size={14} style={{ color: 'var(--brand)' }} />
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{p.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{p.type}</div>
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {generalForm.default_provider === p.name && (
                    <span className="text-[10px] px-2 py-0.5 rounded-full"
                      style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                      default
                    </span>
                  )}
                  <span className="text-[10px] px-2 py-0.5 rounded-full"
                    style={{ background: 'rgba(148, 163, 184, 0.15)', color: '#94a3b8' }}>
                    config.yaml
                  </span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Org-level providers header (shown when there are inherited items) */}
      {platformProviders.length > 0 && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Organization
        </div>
      )}

      {/* Org-level providers (editable) */}
      <ProvidersSettings
        settings={settings}
        providerForms={providerForms}
        setProviderForms={setProviderForms}
        generalForm={generalForm}
        setGeneralForm={setGeneralForm}
        saving={saving}
        setSaving={setSaving}
        setSaveSuccess={setSaveSuccess}
        error={error}
        setError={setError}
        loadData={loadData}
        level="org"
        inheritedProviders={[
          ...platformProviders.map(p => ({ name: p.name, type: p.type, level: 'platform' })),
          ...configFileProviders
            .filter(c => !platformProviders.some(p => p.name === c.name))
            .map(p => ({ name: p.name, type: p.type, level: p.level })),
        ]}
        inheritedDefaults={platformDefaults.provider || platformDefaults.model ? { provider: platformDefaults.provider, model: platformDefaults.model, source: 'Platform' } : undefined}
        onSaveDefault={async (provider, model) => {
          await saveOrgProviders({ default_provider: provider, default_model: model })
          loadData()
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// OrgGeneralView — Org settings: rename organization
// ---------------------------------------------------------------------------

function OrgGeneralView({ org, onOrgUpdated }: { org: { id: string; name: string; slug: string }; onOrgUpdated?: () => void }) {
  const [name, setName] = useState(org.name)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  useEffect(() => { setName(org.name) }, [org.name])

  const handleSave = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) { setError('Name is required'); return }
    setSaving(true); setError(''); setSuccess('')
    try {
      const { updateOrg } = await import('../api/platform')
      await updateOrg(name.trim())
      setSuccess('Organization name updated')
      if (onOrgUpdated) onOrgUpdated()
    } catch (err) {
      setError(errMsg(err, 'Failed to update organization'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="p-6 max-w-xl space-y-6">
      <form onSubmit={handleSave} className="space-y-4">
        <div>
          <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Organization Name</label>
          <input
            type="text"
            value={name}
            onChange={e => setName(e.target.value)}
            className="w-full px-4 py-2.5 rounded-xl text-sm outline-none"
            style={inputStyle}
          />
        </div>
        <div>
          <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Slug</label>
          <input
            type="text"
            value={org.slug}
            disabled
            className="w-full px-4 py-2.5 rounded-xl text-sm outline-none opacity-60"
            style={inputStyle}
          />
          <p className="mt-1 text-xs" style={{ color: 'var(--text-muted)' }}>Slug cannot be changed after creation.</p>
        </div>
        {error && <InlineError msg={error} />}
        {success && <div className="text-sm px-3 py-2 rounded-lg" style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e' }}>{success}</div>}
        <button
          type="submit"
          disabled={saving || name.trim() === org.name}
          className="flex items-center gap-2 px-4 py-2.5 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
          style={gradientPurple}
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : null}
          Save Changes
        </button>
      </form>
    </div>
  )
}

// ---------------------------------------------------------------------------
// OrgTeamsView — Manage teams for the current organization (org admin/owner)
// ---------------------------------------------------------------------------

function OrgTeamsView({ onTeamsChanged }: { onTeamsChanged?: () => void }) {
  const [teams, setTeams] = useState<Team[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [newSlug, setNewSlug] = useState('')
  const [creating, setCreating] = useState(false)
  const [editingTeam, setEditingTeam] = useState<string | null>(null)
  const [editName, setEditName] = useState('')
  const [renaming, setRenaming] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<{ slug: string; name: string; memberCount: number } | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const data = await fetchTeams()
      setTeams(data)
    } catch (err) {
      setError(errMsg(err, 'Failed to load teams'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    if (success) { const t = setTimeout(() => setSuccess(''), 3000); return () => clearTimeout(t) }
  }, [success])
  useEffect(() => {
    if (error) { const t = setTimeout(() => setError(''), 5000); return () => clearTimeout(t) }
  }, [error])

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (!newName.trim()) { setError('Team name is required'); return }
    setCreating(true); setError('')
    try {
      const slug = newSlug.trim() || slugify(newName.trim())
      await createTeam(newName.trim(), slug)
      setSuccess(`Team "${newName.trim()}" created`)
      setShowCreate(false); setNewName(''); setNewSlug('')
      load()
      if (onTeamsChanged) onTeamsChanged()
    } catch (err) {
      setError(errMsg(err, 'Failed to create team'))
    } finally {
      setCreating(false)
    }
  }

  const handleDelete = async (slug: string, name: string, memberCount: number) => {
    setDeleteConfirm({ slug, name, memberCount })
  }

  const confirmDelete = async () => {
    if (!deleteConfirm) return
    const { slug, name } = deleteConfirm
    setDeleteConfirm(null)
    try {
      await deleteTeam(slug)
      setSuccess(`Team "${name}" deleted`)
      load()
      if (onTeamsChanged) onTeamsChanged()
    } catch (err) {
      setError(errMsg(err, 'Failed to delete team'))
    }
  }

  const handleRename = async (slug: string) => {
    if (!editName.trim()) return
    setRenaming(true)
    try {
      const { renameTeam } = await import('../api/platform')
      await renameTeam(slug, editName.trim())
      setSuccess(`Team renamed to "${editName.trim()}"`)
      setEditingTeam(null); setEditName('')
      load()
      if (onTeamsChanged) onTeamsChanged()
    } catch (err) {
      setError(errMsg(err, 'Failed to rename team'))
    } finally {
      setRenaming(false)
    }
  }

  return (
    <div className="p-6">
      {/* Status messages */}
      {error && <div className="mb-4"><InlineError msg={error} /></div>}
      {success && <div className="mb-4 text-sm px-3 py-2 rounded-lg" style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e' }}>{success}</div>}

      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <p className="text-sm" style={{ color: 'var(--text-muted)' }}>Manage teams in your organization. Each team has its own resources, members, and workspace.</p>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 rounded-xl text-sm font-medium text-white hover:opacity-90 shrink-0"
          style={gradientPurple}
        >
          <Plus size={14} /> New Team
        </button>
      </div>

      {/* Teams list */}
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
        </div>
      ) : teams.length === 0 ? (
        <p className="text-center py-12 text-sm" style={{ color: 'var(--text-muted)' }}>No teams yet.</p>
      ) : (
        <div className="space-y-2">
          {teams.map(team => (
            <div key={team.slug} className="flex items-center gap-3 p-3 rounded-xl" style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}>
              {editingTeam === team.slug ? (
                <div className="flex-1 flex items-center gap-2">
                  <input
                    type="text"
                    value={editName}
                    onChange={e => setEditName(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter') handleRename(team.slug); if (e.key === 'Escape') setEditingTeam(null) }}
                    className="flex-1 px-3 py-1.5 rounded-lg text-sm outline-none"
                    style={inputStyle}
                    autoFocus
                  />
                  <button
                    onClick={() => handleRename(team.slug)}
                    disabled={renaming || !editName.trim()}
                    className="px-3 py-1.5 rounded-lg text-xs font-medium text-white hover:opacity-90 disabled:opacity-50"
                    style={gradientPurple}
                  >
                    {renaming ? <Loader2 size={12} className="animate-spin" /> : 'Save'}
                  </button>
                  <button
                    onClick={() => setEditingTeam(null)}
                    className="px-3 py-1.5 rounded-lg text-xs font-medium"
                    style={{ background: 'var(--bg-secondary)', color: 'var(--text-secondary)' }}
                  >
                    Cancel
                  </button>
                </div>
              ) : (
                <>
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{team.name}</div>
                    <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{team.slug}</div>
                  </div>
                  <button
                    onClick={() => { setEditingTeam(team.slug); setEditName(team.name) }}
                    className="px-2.5 py-1 rounded-lg text-xs font-medium transition-colors hover:bg-[var(--bg-secondary)]"
                    style={{ color: 'var(--text-secondary)' }}
                    title="Rename Team"
                  >
                    Rename
                  </button>
                  {teams.length > 1 && (
                    <button
                      onClick={() => handleDelete(team.slug, team.name, team.member_count || 0)}
                      className="p-1.5 rounded-lg transition-opacity hover:opacity-80"
                      style={{ color: 'var(--danger)' }}
                      title="Delete Team"
                    >
                      <Trash2 size={14} />
                    </button>
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Create Team Modal */}
      {showCreate && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => setShowCreate(false)} />
          <div className="relative w-full max-w-md mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
            <div className="px-6 py-5" style={gradientPurple}>
              <h2 className="text-lg font-semibold text-white">Create Team</h2>
              <p className="text-sm text-white/70 mt-0.5">Add a new team to the organization</p>
            </div>
            <form onSubmit={handleCreate} className="p-6 space-y-4">
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Name *</label>
                <input type="text" value={newName} onChange={e => setNewName(e.target.value)} placeholder="Engineering" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} autoFocus />
              </div>
              <div>
                <label className="block text-sm font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Slug <span className="text-xs font-normal" style={{ color: 'var(--text-muted)' }}>(auto-generated if empty)</span></label>
                <input type="text" value={newSlug} onChange={e => setNewSlug(e.target.value)} placeholder="engineering" className="w-full px-4 py-2.5 rounded-xl text-sm outline-none" style={inputStyle} />
              </div>
              <div className="flex gap-3 pt-2">
                <button type="button" onClick={() => setShowCreate(false)} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Cancel</button>
                <button type="submit" disabled={creating} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white hover:opacity-90 disabled:opacity-50" style={gradientPurple}>
                  {creating ? <Loader2 size={16} className="animate-spin" /> : 'Create Team'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Delete Team Confirmation */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h3 className="text-lg font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Delete Team</h3>
            <p className="text-sm mb-4" style={{ color: 'var(--text-muted)' }}>
              {deleteConfirm.memberCount > 0 ? (
                <>There {deleteConfirm.memberCount === 1 ? 'is' : 'are'} <strong style={{ color: 'var(--text-primary)' }}>{deleteConfirm.memberCount} member{deleteConfirm.memberCount !== 1 ? 's' : ''}</strong> in this team. They will be removed.<br /><br /></>
              ) : null}
              Are you sure you want to delete <strong style={{ color: 'var(--text-primary)' }}>{deleteConfirm.name}</strong>? This cannot be undone.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setDeleteConfirm(null)}
                className="px-4 py-2 rounded-lg text-sm font-medium"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
              >
                Cancel
              </button>
              <button
                onClick={confirmDelete}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all"
                style={{ background: '#ef4444' }}
              >
                <Trash2 size={14} /> Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamProvidersTab — wraps ProvidersSettings with team-scoped data + inherited platform/org providers
// ---------------------------------------------------------------------------

function TeamProvidersTab({ teamSlug }: { teamSlug: string }) {
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [providerForms, setProviderForms] = useState<Record<string, Record<string, string>>>({})
  const [generalForm, setGeneralForm] = useState({ default_provider: '', default_model: '' })
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Inherited providers (read-only)
  const [inheritedProviders, setInheritedProviders] = useState<{ name: string; type: string; configured: boolean; level: string }[]>([])
  // Inherited defaults for display
  const [inheritedDefaults, setInheritedDefaults] = useState<{ provider: string; model: string; source: string }>({ provider: '', model: '', source: '' })

  const loadData = useCallback(async () => {
    // Load effective settings for the full provider list
    try {
      const data = await fetchSettings()
      setSettings(data)
      const pForms: Record<string, Record<string, string>> = {}
      data.providers.forEach(p => { pForms[p.name] = { ...p.fields } })
      setProviderForms(pForms)
    } catch (err) {
      console.error('Failed to load team settings:', err)
    }

    // Load raw team-level defaults (no cascade) for generalForm
    try {
      const teamData = await fetchTeamProviders()
      setGeneralForm({
        default_provider: teamData.default_provider || '',
        default_model: teamData.default_model || ''
      })
    } catch {
      // If team providers endpoint fails, defaults stay empty (= "Not Set")
      setGeneralForm({ default_provider: '', default_model: '' })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- reload when active team changes
  }, [teamSlug])

  const loadInheritedProviders = useCallback(async () => {
    const inherited: { name: string; type: string; configured: boolean; level: string }[] = []
    let platformDefaultProvider = ''
    let platformDefaultModel = ''
    let orgDefaultProvider = ''
    let orgDefaultModel = ''

    try {
      const platformData = await fetchPlatformProviders()
      platformDefaultProvider = platformData.default_provider || ''
      platformDefaultModel = platformData.default_model || ''
      if (platformData.providers) {
        for (const [name, fields] of Object.entries(platformData.providers)) {
          const type = fields?.type || name
          const fieldsCopy = { ...fields }
          delete fieldsCopy.type
          inherited.push({ name, type, configured: Object.keys(fieldsCopy).length > 0, level: 'platform' })
        }
      }
    } catch { /* ignore */ }
    try {
      const orgData = await fetchOrgProviders()
      orgDefaultProvider = orgData.default_provider || ''
      orgDefaultModel = orgData.default_model || ''
      if (orgData.providers) {
        for (const [name, fields] of Object.entries(orgData.providers)) {
          const type = fields?.type || name
          const fieldsCopy = { ...fields }
          delete fieldsCopy.type
          // Don't duplicate if already inherited from platform
          if (!inherited.find(i => i.name === name)) {
            inherited.push({ name, type, configured: Object.keys(fieldsCopy).length > 0, level: 'org' })
          }
        }
      }
    } catch { /* ignore */ }
    setInheritedProviders(inherited)

    // Compute effective inherited defaults (org wins over platform)
    const effectiveProvider = orgDefaultProvider || platformDefaultProvider
    const effectiveModel = orgDefaultModel || platformDefaultModel
    const source = orgDefaultProvider || orgDefaultModel ? 'Org' : 'Platform'
    setInheritedDefaults({ provider: effectiveProvider, model: effectiveModel, source })
  }, [])

  useEffect(() => { loadData(); loadInheritedProviders() }, [loadData, loadInheritedProviders])

  // Team-only providers (exclude inherited ones from the editable list)
  const inheritedNames = new Set(inheritedProviders.map(p => p.name))
  const teamOnlySettings: SettingsData | null = settings ? {
    ...settings,
    providers: settings.providers.filter(p => !inheritedNames.has(p.name))
  } : null

  return (
    <div className="space-y-6">
      {/* Inherited providers (read-only) */}
      {inheritedProviders.length > 0 && (
        <div>
          <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
            Inherited
          </div>
          <div className="space-y-2">
            {inheritedProviders.map(p => (
              <div
                key={p.name}
                className="flex items-center justify-between p-3 rounded-lg"
                style={{ background: 'var(--card)', border: '1px solid var(--border-color)' }}
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
                    style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                    <Key size={14} style={{ color: 'var(--brand)' }} />
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>{p.name}</div>
                    <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{p.type}</div>
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {generalForm.default_provider === p.name && (
                    <span className="text-[10px] px-2 py-0.5 rounded-full"
                      style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                      default
                    </span>
                  )}
                  <span className="text-[10px] px-2 py-0.5 rounded-full"
                    style={{ background: p.level === 'platform' ? 'rgba(59, 130, 246, 0.15)' : 'rgba(34, 197, 94, 0.15)', color: p.level === 'platform' ? '#3b82f6' : '#22c55e' }}>
                    {p.level}
                  </span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Team-level providers header (shown when there are inherited items) */}
      {inheritedProviders.length > 0 && (
        <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          Team
        </div>
      )}

      {/* Team-level providers (editable) */}
      <ProvidersSettings
        settings={teamOnlySettings}
        providerForms={providerForms}
        setProviderForms={setProviderForms}
        generalForm={generalForm}
        setGeneralForm={setGeneralForm}
        saving={saving}
        setSaving={setSaving}
        setSaveSuccess={setSaveSuccess}
        error={error}
        setError={setError}
        loadData={loadData}
        level="team"
        inheritedProviders={inheritedProviders.map(p => ({ name: p.name, type: p.type, level: p.level }))}
        inheritedDefaults={inheritedDefaults.provider || inheritedDefaults.model ? inheritedDefaults : undefined}
        onSaveDefault={async (provider, model) => {
          // Load current team settings to preserve other general fields (web tools, etc.)
          try {
            const current = await fetchSettings()
            await saveTeamSettings({
              general: {
                ...current.general,
                default_provider: provider,
                default_model: model
              }
            })
          } catch {
            // Fallback: just save defaults (may clear other fields if settings can't be loaded)
            await saveTeamSettings({ general: { default_provider: provider, default_model: model } })
          }
          loadData()
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamContent — renders the right sub-component for each team tab
// ---------------------------------------------------------------------------

interface TeamContentProps {
  tabId: string
  teamSlug: string
  theme: string
  user: { id: string; email: string; display_name: string; role: string }
  canManageTeam: boolean
  team: Team | null
}

function TeamContent({ tabId, teamSlug, theme, user, canManageTeam, team }: TeamContentProps) {
  const [fullConfig, setFullConfig] = useState<any>(null)
  const [fullConfigLoading, setFullConfigLoading] = useState(false)

  const needsFullConfig = ['skills'].includes(tabId)

  useEffect(() => {
    if (!needsFullConfig || fullConfig) return
    let cancelled = false
    const load = async () => {
      setFullConfigLoading(true)
      try {
        const { fetchFullConfig } = await import('./settings/settingsApi')
        const data = await fetchFullConfig()
        if (!cancelled) setFullConfig(data)
      } catch (err) {
        console.error('Failed to load config:', err)
      } finally {
        if (!cancelled) setFullConfigLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [needsFullConfig, fullConfig])

  const handleSaved = () => setFullConfig(null)

  if (tabId === 'members' && team) {
    return <MembersPanel user={user} team={team} canManageTeam={canManageTeam} />
  }

  if (tabId === 'knowledge') {
    return (
      <div className="flex-1 overflow-hidden p-6 flex flex-col">
        <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
          <KnowledgeBrowser theme={theme as 'dark' | 'light'} user={user} activeTeam={teamSlug} />
        </Suspense>
      </div>
    )
  }

  if (tabId === 'container') {
    return (
      <div className="flex-1 overflow-hidden p-6 flex flex-col">
        <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
          <TeamContainerTab teamSlug={teamSlug} theme={theme as 'dark' | 'light'} canManage={canManageTeam} />
        </Suspense>
      </div>
    )
  }

  if (fullConfigLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
        <span className="ml-2 text-sm" style={{ color: 'var(--text-muted)' }}>Loading...</span>
      </div>
    )
  }

  // Skills tab needs full viewport height for its embedded full-screen editor
  if (tabId === 'skills') {
    return (
      <div className="flex-1 overflow-hidden p-6 flex flex-col">
        <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
          <SkillsSettings config={fullConfig?.skills || null} onSaved={handleSaved} theme={theme} isPlatform canManage={canManageTeam} teamSlug={teamSlug} />
        </Suspense>
      </div>
    )
  }

  return (
    <div className="flex-1 overflow-y-auto p-6">
      <Suspense fallback={<div className="flex items-center justify-center py-12"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
        {tabId === 'providers' && <TeamProvidersTab teamSlug={teamSlug} />}
        {tabId === 'mcp' && <TeamMCPServersTab teamSlug={teamSlug} theme={theme} />}
        {tabId === 'a2a' && <TeamA2AAgentsTab teamSlug={teamSlug} theme={theme} />}
        {tabId === 'network' && <TeamNetworkPolicyTab teamSlug={teamSlug} />}
        {tabId === 'taps' && <TapsSettings teamSlug={teamSlug} />}
        {tabId === 'flows' && <FlowStorePanel teamSlug={teamSlug} canManage={canManageTeam} />}
      </Suspense>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main SettingsPage
// ---------------------------------------------------------------------------

export default function SettingsPage({
  activeSection: externalActiveSection,
  onSectionChange,
  onToolsRefresh,
  onSettingsSaved,
  updateAvailable = null,
  onUpdateClick = null,
  appVersion = 'dev',
  theme = 'dark',
  isPlatformMode = false,
  userRole = '',
  platformRole = '',
  user = null,
  org = null,
  activeTeam = null,
  teams = null,
  teamSlug: urlTeamSlug = '',
  teamTab: urlTeamTab = 'members',
  subsection = '',
}: SettingsPageProps) {
  const isAdmin = userRole === 'admin' || userRole === 'owner'
  const isSuperadmin = platformRole === 'superadmin'

  // --- Team state ---
  const [allTeams, setAllTeams] = useState<Team[]>([])
  const [teamsLoading, setTeamsLoading] = useState(false)
  const [teamsError, setTeamsError] = useState('')
  const [callerRoles, setCallerRoles] = useState<Record<string, string>>({})

  // Resolve which team is selected — use activeTeam from TopBar context (no separate picker)
  const resolvedTeamSlug = urlTeamSlug || activeTeam || (allTeams.length === 1 ? allTeams[0].slug : '') || ''
  const selectedTeam = allTeams.find(t => t.slug === resolvedTeamSlug) || null
  const activeTeamName = teams?.find(t => t.slug === resolvedTeamSlug)?.name || selectedTeam?.name || resolvedTeamSlug

  // Load teams in platform mode
  const loadTeams = useCallback(async () => {
    if (!isPlatformMode) return
    setTeamsLoading(true); setTeamsError('')
    try {
      const data = await fetchTeams()
      setAllTeams(data)
    } catch (err) { setTeamsError(errMsg(err, 'Failed to load teams')) }
    finally { setTeamsLoading(false) }
  }, [isPlatformMode])

  useEffect(() => {
    if (isPlatformMode) loadTeams()
  }, [isPlatformMode, loadTeams])

  // Load caller role for selected team (only if team exists in loaded teams list)
  useEffect(() => {
    if (!resolvedTeamSlug || callerRoles[resolvedTeamSlug]) return
    if (allTeams.length > 0 && !allTeams.find(t => t.slug === resolvedTeamSlug)) return
    let cancelled = false
    const load = async () => {
      try {
        const resp = await fetchTeamMembers(resolvedTeamSlug)
        if (!cancelled) setCallerRoles(prev => ({ ...prev, [resolvedTeamSlug]: resp.callerRole }))
      } catch { /* ignore */ }
    }
    load()
    return () => { cancelled = true }
  }, [resolvedTeamSlug, callerRoles, allTeams])

  const canManageTeam = isAdmin || callerRoles[resolvedTeamSlug] === 'admin' || callerRoles[resolvedTeamSlug] === 'org_admin'

  // --- Build sidebar categories ---
  // Admin-only team items (hidden from regular members)
  const adminOnlyTeamItems = new Set(['team-providers', 'team-mcp', 'team-a2a', 'team-taps', 'team-container'])
  const memberTeamItems = canManageTeam ? TEAM_ITEMS : TEAM_ITEMS.filter(item => !adminOnlyTeamItems.has(item.id))

  // Platform section (superadmin only) — merges former "System" items.
  // Org admins/owners see Personal + Team + Organization (no Platform/System).
  const categories: MenuCategory[] = isSuperadmin
    ? [
        { label: 'Personal', items: PERSONAL_ITEMS },
        { label: activeTeamName ? `Team — ${activeTeamName}` : 'Team', items: TEAM_ITEMS },
        { label: 'Organization', items: ORG_ITEMS },
        { label: 'Platform', items: PLATFORM_ITEMS },
      ]
    : isAdmin
      ? [
          { label: 'Personal', items: PERSONAL_ITEMS },
          { label: activeTeamName ? `Team — ${activeTeamName}` : 'Team', items: TEAM_ITEMS },
          { label: 'Organization', items: ORG_ITEMS },
        ]
      : [
          { label: 'Personal', items: PERSONAL_ITEMS },
          { label: activeTeamName ? `Team — ${activeTeamName}` : 'Team', items: memberTeamItems },
        ]

  // --- Resolve active section ---
  const allItems = categories.flatMap(c => c.items)
  // Map URL section params to internal section IDs
  let resolvedSection = externalActiveSection || 'chat'
  if (resolvedSection === 'team') {
    resolvedSection = `team-${urlTeamTab || 'members'}`
  } else if (resolvedSection === 'org') {
    resolvedSection = `org-${subsection || 'general'}`
  } else if (resolvedSection === 'platform') {
    resolvedSection = `platform-${subsection || 'orgs'}`
  }

  const activeSection = allItems.some(i => i.id === resolvedSection) ? resolvedSection : (allItems[0]?.id || 'channels')
  const activeLabel = allItems.find(item => item.id === activeSection)?.label || ''

  // Determine which data to load (for system-level settings sections).
  // Platform-prefixed system sections (e.g., 'platform-chat') map to their
  // SettingsContent IDs (e.g., 'chat') for data loading and rendering.
  const settingsContentSection = PLATFORM_SYSTEM_SECTIONS[activeSection] ||
    (!activeSection.startsWith('team-') && !activeSection.startsWith('org-') && !activeSection.startsWith('platform-') ? activeSection : '')
  const isSystemSection = settingsContentSection !== ''
  const data = useSettingsData(settingsContentSection)

  // Navigate to a team tab with slug
  const navigateTeamTab = (tab: string, slug?: string) => {
    if (onSectionChange) {
      // We encode team navigation as a special format the parent can parse
      // The parent (App.tsx) will build the correct URL
      onSectionChange(`team/${slug || resolvedTeamSlug}/${tab}`)
    }
  }

  // --- Loading state ---
  if (isSystemSection && data.loading) {
    return (
      <div className="flex h-full items-center justify-center" style={{ background: 'var(--work-background, var(--bg-primary))' }}>
        <div style={{ color: 'var(--text-muted)' }}>Loading settings...</div>
      </div>
    )
  }

  return (
    <div className="flex h-full" style={{ background: 'var(--work-background, var(--bg-primary))' }}>
      {/* Left Sidebar — same dark rail language as Chat */}
      <div className="w-64 border-r flex flex-col shrink-0" style={{ background: 'var(--work-sidebar, var(--sidebar-background))', borderColor: 'var(--border-color)' }}>
        {/* Header */}
        <div className="p-4 border-b flex items-center justify-between" style={{ borderColor: 'var(--border-color)' }}>
          <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Settings</h2>
        </div>

        {/* Menu items with category headers */}
        <nav className="flex-1 p-2 space-y-0.5 overflow-y-auto">
          {categories.map((category, catIdx) => (
            <div key={catIdx}>
              {category.label && (
                <div className="px-3 pt-4 pb-1.5 flex items-center justify-between">
                  <span className="text-[11px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-faint, var(--text-muted))' }}>
                    {category.label}
                  </span>
                </div>
              )}

              {category.items.map(item => {
                const Icon = item.icon
                const isActive = activeSection === item.id
                return (
                  <button
                    key={item.id}
                    onClick={() => {
                      if (item.id.startsWith('team-')) {
                        const tab = item.id.replace('team-', '')
                        navigateTeamTab(tab, resolvedTeamSlug)
                      } else if (item.id.startsWith('org-')) {
                        if (onSectionChange) onSectionChange(`org/${item.id.replace('org-', '')}`)
                      } else if (item.id.startsWith('platform-')) {
                        if (onSectionChange) onSectionChange(`platform/${item.id.replace('platform-', '')}`)
                      } else {
                        if (onSectionChange) onSectionChange(item.id)
                      }
                    }}
                    className="w-full flex items-center gap-3 px-3 py-2 rounded-[var(--radius-md)] transition-all"
                    style={{
                      background: isActive ? 'var(--item-active)' : 'transparent',
                      color: isActive ? 'var(--text-primary)' : 'var(--text-secondary)',
                      border: `1px solid ${isActive ? 'color-mix(in oklab, var(--brand) 28%, transparent)' : 'transparent'}`,
                    }}
                  >
                    <Icon size={18} style={{ color: isActive ? 'var(--brand)' : undefined }} />
                    <span className="font-medium text-sm">{item.label}</span>
                    {isActive && <ChevronRight size={16} className="ml-auto" style={{ color: 'var(--brand)' }} />}
                  </button>
                )
              })}
            </div>
          ))}
        </nav>

        {/* Update Available Indicator */}
        {updateAvailable && onUpdateClick && (
          <div className="p-3 border-t" style={{ borderColor: 'var(--border-color)' }}>
            <button
              onClick={onUpdateClick}
              className="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg transition-all hover:scale-[1.02]"
              style={{
                background: 'linear-gradient(135deg, var(--brand-muted) 0%, color-mix(in oklab, var(--brand) 20%, transparent) 100%)',
                border: '1px solid color-mix(in oklab, var(--brand) 30%, transparent)'
              }}
            >
              <Download size={18} style={{ color: 'var(--brand)' }} />
              <div className="flex-1 text-left">
                <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Update Available</div>
                <div className="text-xs" style={{ color: 'var(--text-muted)' }}>{updateAvailable.version}</div>
              </div>
            </button>
          </div>
        )}

        {/* Version Info */}
        <div className="p-3 border-t text-xs space-y-1" style={{ borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}>
          <div className="opacity-60">App Version: {appVersion}</div>
          <div className="opacity-60">UI Version: {__UI_VERSION__}</div>
        </div>
      </div>

      {/* Right Content — solid work canvas (no ambient glow) */}
      <div className="flex-1 flex flex-col overflow-hidden" style={{ background: 'var(--work-background, var(--bg-primary))' }}>
        {/* Header */}
        <div className="p-4 border-b flex items-center justify-between" style={{ borderColor: 'var(--border-color)', background: 'var(--work-background, var(--bg-primary))' }}>
          <h3 className="text-xl font-semibold" style={{ color: 'var(--text-primary)' }}>
            {activeLabel}
          </h3>
        </div>

        {/* Content */}
        <div className={activeSection === 'mcp' || activeSection === 'team-mcp' || activeSection === 'knowledge' || activeSection.endsWith('-skills') || activeSection === 'skills' ? 'flex-1 overflow-hidden flex flex-col' : 'flex-1 overflow-y-auto flex flex-col'}>
          {/* Team sections */}
          {activeSection.startsWith('team-') && resolvedTeamSlug && selectedTeam && user && (
            <TeamContent
              tabId={activeSection.replace('team-', '')}
              teamSlug={resolvedTeamSlug}
              theme={theme}
              user={user}
              canManageTeam={canManageTeam}
              team={selectedTeam}
            />
          )}

          {activeSection.startsWith('team-') && (!resolvedTeamSlug || !selectedTeam) && !teamsLoading && (
            <div className="flex items-center justify-center h-full">
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                {teamsError || 'No team selected'}
              </p>
            </div>
          )}

          {activeSection.startsWith('team-') && teamsLoading && (
            <div className="flex items-center justify-center py-12">
              <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
            </div>
          )}

          {/* Organization sections */}
          {activeSection === 'org-general' && org && (
            <OrgGeneralView org={org} onOrgUpdated={() => { /* could refresh org context */ }} />
          )}

          {activeSection === 'org-teams' && (
            <OrgTeamsView onTeamsChanged={() => { loadTeams() }} />
          )}

          {activeSection === 'org-users' && user && org && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <UserManagement theme={theme as 'dark' | 'light'} user={user} org={org} />
            </Suspense>
          )}

          {activeSection === 'org-audit' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <AuditViewer theme={theme as 'dark' | 'light'} />
            </Suspense>
          )}

          {activeSection === 'org-skills' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="flex-1 overflow-hidden p-6 flex flex-col h-full">
                <SkillsSettings config={null} onSaved={() => {}} theme={theme} scope="org" isPlatform canManage={isAdmin} teamSlug={resolvedTeamSlug} />
              </div>
            </Suspense>
          )}

          {activeSection === 'org-mcp' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6 h-full">
                <OrgMCPServersTab theme={theme} />
              </div>
            </Suspense>
          )}

          {activeSection === 'org-a2a' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6 h-full">
                <OrgA2AAgentsTab theme={theme} />
              </div>
            </Suspense>
          )}

          {activeSection === 'org-network' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6">
                <OrgNetworkPolicyTab />
              </div>
            </Suspense>
          )}

          {activeSection === 'org-providers' && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6">
                <OrgProvidersTab />
              </div>
            </Suspense>
          )}

          {/* Platform sections */}
          {activeSection === 'platform-providers' && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6">
                <PlatformProvidersTab />
              </div>
            </Suspense>
          )}
          {activeSection === 'platform-mcp' && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <PlatformMCPServersTab theme={theme as string} />
            </Suspense>
          )}
          {activeSection === 'platform-a2a' && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6 h-full">
                <PlatformA2AAgentsTab theme={theme as string} />
              </div>
            </Suspense>
          )}
          {activeSection === 'platform-network' && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="p-6">
                <PlatformNetworkPolicyTab />
              </div>
            </Suspense>
          )}
          {activeSection === 'platform-skills' && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <div className="flex-1 overflow-hidden p-6 flex flex-col h-full">
                <SkillsSettings config={null} onSaved={() => {}} theme={theme} scope="platform" isPlatform canManage={true} />
              </div>
            </Suspense>
          )}
          {activeSection.startsWith('platform-') && activeSection !== 'platform-providers' && activeSection !== 'platform-mcp' && activeSection !== 'platform-network' && activeSection !== 'platform-skills' && !PLATFORM_SYSTEM_SECTIONS[activeSection] && isSuperadmin && (
            <Suspense fallback={<div className="flex items-center justify-center h-full"><Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} /></div>}>
              <PlatformAdminPanel theme={theme as 'dark' | 'light'} activeTab={activeSection.replace('platform-', '')} />
            </Suspense>
          )}

          {/* System / platform config sections (delegated to SettingsContent) */}
          {isSystemSection && (
            <div className={settingsContentSection === 'mcp' || settingsContentSection === 'knowledge' || settingsContentSection === 'skills' ? 'h-full flex flex-col' : 'p-6'}>
              <SettingsContent
                activeSection={settingsContentSection}
                settings={data.settings}
                mcpConfig={data.mcpConfig}
                webCapableTools={data.webCapableTools}
                standardServers={data.standardServers}
                loadData={data.loadData}
                fullConfig={data.fullConfig}
                fullConfigLoading={data.fullConfigLoading}
                invalidateFullConfig={data.invalidateFullConfig}
                onToolsRefresh={onToolsRefresh}
                onSettingsSaved={onSettingsSaved}
                onSectionChange={onSectionChange}
                theme={theme}
                isPlatformMode={isPlatformMode}
                isOrgAdmin={isAdmin}
                user={user || undefined}
                activeTeam={activeTeam}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
