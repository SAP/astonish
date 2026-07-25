import { useState, useEffect, useCallback, useRef } from 'react'
import { Save, AlertCircle, Check, Plus, Trash2, ChevronRight, ChevronDown, Eye, Pencil, X, Loader2, CheckCircle, XCircle, FileText, FolderOpen, ShieldAlert, ShieldCheck } from 'lucide-react'
import { saveFullConfigSection, inputClass, inputStyle, labelStyle, hintStyle, sectionBorderStyle, saveButtonStyle } from './settingsApi'
import { teamFetch } from '../../api/teamContext'
import CodeMirror from '@uiw/react-codemirror'
import { markdown } from '@codemirror/lang-markdown'
import { languages } from '@codemirror/language-data'
import { javascript } from '@codemirror/lang-javascript'
import { python } from '@codemirror/lang-python'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'

// --- Types ---

interface Skill {
  name: string
  description: string
  source: string
  scope?: string // "platform", "org", "team"
  eligible: boolean
  editable: boolean
  require_bins?: string[]
  missing?: string[]
  has_directory?: boolean   // New: indicates the skill has auxiliary files
  validation_status?: string // "unknown", "clean", "warnings", "blocked", "acknowledged"
}

interface ActiveSkill {
  name: string
  content: string
  raw_file: string
  editable: boolean
  source: string
  scope?: string
  description?: string
  files?: Array<{ path: string; filename: string; size?: number; is_executable?: boolean }>
  validation_status?: string
  validation?: { issues: any[] }
  acknowledged_risks?: Array<{ message: string; type: string; acknowledged_by: string; acknowledged_by_email: string; acknowledged_at: string; content_hash: string }>
}

interface SkillsForm {
  enabled: boolean
  user_dir: string
  extra_dirs: string[]
  allowlist: string[]
}

interface SkillsSettingsProps {
  config: Record<string, any> | null
  onSaved?: () => void
  theme?: string
  /** Scope filter for API calls: "team", "org", or undefined (merged) */
  scope?: string
  /** Whether running in platform mode */
  isPlatform?: boolean
  /** Whether the user can manage skills in this context */
  canManage?: boolean
  /** Explicit team slug — overrides global active team for API calls */
  teamSlug?: string
}

// API helpers — use teamFetch for team header injection in platform mode
// teamSlug parameter overrides the global active team when provided
const fetchSkills = async (scope?: string, teamSlug?: string): Promise<{ skills: Skill[], is_team_admin: boolean, is_org_admin: boolean }> => {
  const params = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/skills${params}`, undefined, teamSlug)
  if (!res.ok) throw new Error('Failed to fetch skills')
  return res.json()
}

const fetchSkillContent = async (name: string, scope?: string, teamSlug?: string): Promise<ActiveSkill> => {
  const params = scope ? `?scope=${scope}` : ''
  const [contentRes, filesRes] = await Promise.all([
    teamFetch(`/api/skills/${encodeURIComponent(name)}/content${params}`, undefined, teamSlug),
    teamFetch(`/api/skills/${encodeURIComponent(name)}/files${params}`, undefined, teamSlug)
  ])

  if (!contentRes.ok) throw new Error('Failed to fetch skill content')

  const data = await contentRes.json()

  // Attach files list if available
  if (filesRes.ok) {
    try {
      const filesData = await filesRes.json()
      if (filesData.files && Array.isArray(filesData.files)) {
        data.files = filesData.files
      }
    } catch {
      // ignore files fetch failure
    }
  }

  return data
}

const saveSkillContent = async (name: string, rawFile: string, scope?: string, teamSlug?: string): Promise<Record<string, any>> => {
  const params = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/skills/${encodeURIComponent(name)}/content${params}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ raw_file: rawFile })
  }, teamSlug)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error(data?.error || 'Failed to save skill')
  }
  return data || {}
}

const createSkillApi = async (name: string, scope?: string, teamSlug?: string): Promise<Record<string, any>> => {
  const res = await teamFetch('/api/skills', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, scope: scope || '' })
  }, teamSlug)
  if (!res.ok) {
    const data = await res.json().catch(() => null)
    throw new Error(data?.error || 'Failed to create skill')
  }
  return res.json()
}

const deleteSkillApi = async (name: string, scope?: string, teamSlug?: string): Promise<Record<string, any>> => {
  const params = scope ? `?scope=${scope}` : ''
  const res = await teamFetch(`/api/skills/${encodeURIComponent(name)}${params}`, {
    method: 'DELETE'
  }, teamSlug)
  if (!res.ok) {
    const data = await res.json().catch(() => null)
    throw new Error(data?.error || 'Failed to delete skill')
  }
  return res.json()
}

// Safe query string builder for skill file endpoints (prevents malformed &path=... when no scope)
const buildSkillFileUrl = (name: string, path: string, filename: string, scope?: string): string => {
  const params = new URLSearchParams()
  if (scope) params.set('scope', scope)
  params.set('path', path)
  params.set('filename', filename)
  return `/api/skills/${encodeURIComponent(name)}/file?${params.toString()}`
}

const deleteSkillFileApi = async (name: string, path: string, filename: string, scope?: string, teamSlug?: string): Promise<void> => {
  const url = buildSkillFileUrl(name, path, filename, scope)
  const res = await teamFetch(url, { method: 'DELETE' }, teamSlug)
  if (!res.ok) {
    const data = await res.json().catch(() => null)
    throw new Error(data?.error || 'Failed to delete file')
  }
}

// Scope badge colors
function ScopeBadge({ scope }: { scope: string }) {
  const colors: Record<string, { bg: string, fg: string }> = {
    team: { bg: 'rgba(34, 197, 94, 0.15)', fg: '#22c55e' },
    org: { bg: 'rgba(59, 130, 246, 0.15)', fg: '#3b82f6' },
    platform: { bg: 'var(--brand-muted)', fg: 'var(--brand)' },
    custom: { bg: 'rgba(34, 197, 94, 0.15)', fg: '#22c55e' },
  }
  const c = colors[scope] || colors.custom
  return (
    <span className="text-[10px] px-1.5 py-0.5 rounded" style={{ background: c.bg, color: c.fg }}>
      {scope}
    </span>
  )
}

export default function SkillsSettings({ config, onSaved, theme = 'dark', scope, isPlatform = false, canManage, teamSlug }: SkillsSettingsProps) {
  // Config form state (only used in personal mode or org admin settings)
  const [form, setForm] = useState<SkillsForm>({
    enabled: true,
    user_dir: '',
    extra_dirs: [],
    allowlist: []
  })
  const [extraDirsText, setExtraDirsText] = useState('')
  const [allowlistText, setAllowlistText] = useState('')

  // Skills list state
  const [skillsList, setSkillsList] = useState<Skill[]>([])
  const [skillsLoading, setSkillsLoading] = useState(false)
  const [skillsError, setSkillsError] = useState<string | null>(null)
  const [isTeamAdmin, setIsTeamAdmin] = useState(false)
  const [isOrgAdmin, setIsOrgAdmin] = useState(false)

  // Editor state
  const [activeSkill, setActiveSkill] = useState<ActiveSkill | null>(null)
  const [editorContent, setEditorContent] = useState('')
  const [loadedContent, setLoadedContent] = useState('') // Track original content for dirty detection
  const [editorMode, setEditorMode] = useState<'view' | 'edit' | null>(null)
  const [editorLoading, setEditorLoading] = useState(false)
  const [editorSaving, setEditorSaving] = useState(false)
  const [editorError, setEditorError] = useState<string | null>(null)
  const [editorSuccess, setEditorSuccess] = useState(false)
  const [acknowledging, setAcknowledging] = useState<string | null>(null) // issue key being acknowledged
  const saveIdRef = useRef(0) // Monotonic counter to discard stale save completions
  const timersRef = useRef<Set<ReturnType<typeof setTimeout>>>(new Set())

  // Cleanup pending timers on unmount
  useEffect(() => {
    const timers = timersRef.current
    return () => { timers.forEach(id => clearTimeout(id)) }
  }, [])

  // Multi-file editor state
  const [currentFilePath, setCurrentFilePath] = useState('')      // '' for SKILL.md
  const [currentFilename, setCurrentFilename] = useState('SKILL.md')

  // Sidebar add-file form
  const [showAddFile, setShowAddFile] = useState(false)
  const [newFileName, setNewFileName] = useState('')
  const [addFileError, setAddFileError] = useState<string | null>(null)

  // Inline delete confirmation for auxiliary files
  const [pendingDeleteFile, setPendingDeleteFile] = useState<{ path: string; filename: string } | null>(null)

  // Validation state
  const [validating, setValidating] = useState(false)
  const [validationResults, setValidationResults] = useState<any[] | null>(null)
  const [validationVisible, setValidationVisible] = useState(false)
  const [validationBlocked, setValidationBlocked] = useState(false) // True when skill has unresolved critical issues

  // Choose CodeMirror language extension based on current file
  const getLanguageExtension = (filename: string) => {
    const lower = filename.toLowerCase()
    if (lower.endsWith('.sh') || lower.endsWith('.bash')) return []
    if (lower.endsWith('.py')) return python()
    if (lower.endsWith('.js') || lower.endsWith('.ts') || lower.endsWith('.jsx') || lower.endsWith('.tsx')) return javascript()
    if (lower.endsWith('.json')) return javascript()
    if (lower.endsWith('.md')) return markdown({ codeLanguages: languages })
    return markdown({ codeLanguages: languages })
  }

  // Create skill modal
  const [showCreate, setShowCreate] = useState(false)
  const [newSkillName, setNewSkillName] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  // Delete confirm
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)
  const [deleteScope, setDeleteScope] = useState<string | undefined>(undefined)

  // Config save state
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Determine if config section should be shown
  // Only in personal mode (no scope filter), or no scope provided
  const showConfig = !isPlatform && !scope

  // Effective management permission: from props, or derived from API response
  const effectiveCanManage = canManage !== undefined
    ? canManage
    : scope === 'team' ? isTeamAdmin : scope === 'org' ? isOrgAdmin : true

  // Dirty tracking: Save button only enabled when content has changed
  const isDirty = editorContent !== loadedContent
  const isDirtyRef = useRef(isDirty)
  useEffect(() => { isDirtyRef.current = isDirty }, [isDirty])

  // Protect unsaved changes from accidental browser navigation
  useEffect(() => {
    const handler = (e: BeforeUnloadEvent) => {
      if (isDirtyRef.current) {
        e.preventDefault()
      }
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [])

  useEffect(() => {
    if (config && showConfig) {
      setForm({
        enabled: config.enabled !== false,
        user_dir: config.user_dir || '',
        extra_dirs: config.extra_dirs || [],
        allowlist: config.allowlist || []
      })
      setExtraDirsText((config.extra_dirs || []).join(', '))
      setAllowlistText((config.allowlist || []).join(', '))
    }
  }, [config, showConfig])

  const loadSkills = useCallback(async () => {
    setSkillsLoading(true)
    setSkillsError(null)
    try {
      const data = await fetchSkills(scope, teamSlug)
      setSkillsList(data.skills || [])
      setIsTeamAdmin(data.is_team_admin ?? false)
      setIsOrgAdmin(data.is_org_admin ?? false)
    } catch (err: any) {
      setSkillsError(err.message)
    } finally {
      setSkillsLoading(false)
    }
  }, [scope, teamSlug])

  useEffect(() => {
    loadSkills()
  }, [loadSkills])

  const handleSaveConfig = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      const saveData = {
        ...form,
        extra_dirs: extraDirsText.split(',').map(s => s.trim()).filter(Boolean),
        allowlist: allowlistText.split(',').map(s => s.trim()).filter(Boolean)
      }
      await saveFullConfigSection('skills', saveData as unknown as Record<string, unknown>)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      const t = setTimeout(() => { setSaveSuccess(false); timersRef.current.delete(t) }, 2000)
      timersRef.current.add(t)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleOpenSkill = async (name: string, mode: 'view' | 'edit', skillScope?: string) => {
    setEditorLoading(true)
    setEditorError(null)
    setEditorSuccess(false)
    setCurrentFilePath('')
    setCurrentFilename('SKILL.md')
    setShowAddFile(false)
    setPendingDeleteFile(null)
    setAddFileError(null)
    setValidationResults(null)
    setValidationVisible(false)
    setValidationBlocked(false)
    try {
      const data = await fetchSkillContent(name, skillScope || scope, teamSlug)
      setActiveSkill(data)
      setEditorContent(data.raw_file)
      setLoadedContent(data.raw_file)
      setEditorMode(mode)
      // Show persisted validation issues if they exist
      if (data.validation?.issues && data.validation.issues.length > 0) {
        setValidationResults(data.validation.issues)
        setValidationVisible(true)
        // Only mark as blocked if there are unacknowledged critical issues
        const acks = data.acknowledged_risks || []
        const hasUnackedCritical = data.validation.issues.some((i: any) =>
          i.severity === 'critical' && !acks.some((a: any) => a.message === i.message && a.type === i.type)
        )
        setValidationBlocked(hasUnackedCritical)
      }
    } catch (err: any) {
      setEditorError(err.message)
    } finally {
      setEditorLoading(false)
    }
  }

  // Switch to editing a different file within the current skill (MVP multi-file support)
  const switchToFile = async (path: string, filename: string) => {
    if (!activeSkill) return

    // Guard: warn user about unsaved changes before switching
    if (editorContent !== loadedContent) {
      if (!window.confirm('You have unsaved changes. Discard and switch files?')) return
    }

    setEditorLoading(true)
    setEditorError(null)

    try {
      const url = buildSkillFileUrl(activeSkill.name, path, filename, activeSkill.scope)
      const res = await teamFetch(url, undefined, teamSlug)
      if (!res.ok) throw new Error('Failed to load file')

      const fileData = await res.json()
      setCurrentFilePath(path)
      setCurrentFilename(filename)
      setEditorContent(fileData.content || '')
      setLoadedContent(fileData.content || '')
    } catch (err: any) {
      setEditorError(err.message)
    } finally {
      setEditorLoading(false)
    }
  }

  // Add a new auxiliary file from the sidebar form
  const handleAddFile = async () => {
    if (!activeSkill || !newFileName.trim()) return
    setAddFileError(null)
    const raw = newFileName.trim()
    const lastSlash = raw.lastIndexOf('/')
    const p = lastSlash > 0 ? raw.substring(0, lastSlash) : ''
    const fn = lastSlash > 0 ? raw.substring(lastSlash + 1) : raw

    if (!fn) {
      setAddFileError('Path must end with a filename')
      return
    }

    try {
      const url = buildSkillFileUrl(activeSkill.name, p, fn, activeSkill.scope)
      await teamFetch(
        url,
        { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content: '' }) },
        teamSlug
      )
      // Refresh
      const refreshed = await fetchSkillContent(activeSkill.name, activeSkill.scope || scope, teamSlug)
      setActiveSkill(refreshed)
      await switchToFile(p, fn)
      // Reset form + any pending delete
      setNewFileName('')
      setShowAddFile(false)
      setAddFileError(null)
      setPendingDeleteFile(null)
    } catch (e: any) {
      setAddFileError(e.message)
    }
  }

  // Delete an auxiliary file (called after inline confirmation)
  const handleDeleteFile = async (path: string, filename: string) => {
    if (!activeSkill) return
    try {
      await deleteSkillFileApi(activeSkill.name, path, filename, activeSkill.scope || scope, teamSlug)
      setPendingDeleteFile(null)

      // Refresh file list
      const refreshed = await fetchSkillContent(activeSkill.name, activeSkill.scope || scope, teamSlug)
      setActiveSkill(refreshed)

      // If we just deleted the file we were viewing, switch back to SKILL.md
      if (currentFilePath === path && currentFilename === filename) {
        setCurrentFilePath('')
        setCurrentFilename('SKILL.md')
        setEditorContent(refreshed.raw_file || '')
        setLoadedContent(refreshed.raw_file || '')
      }
    } catch (e: any) {
      setEditorError(e.message)
      setPendingDeleteFile(null)
    }
  }

  const handleSaveSkill = async () => {
    if (!activeSkill) return
    const currentSaveId = ++saveIdRef.current
    setEditorSaving(true)
    setEditorError(null)
    setEditorSuccess(false)
    const savedContent = editorContent // Snapshot content at save time
    try {
      if (currentFilename === 'SKILL.md' && currentFilePath === '') {
        // Saving the main SKILL.md — validation runs post-save on backend
        const result = await saveSkillContent(
          activeSkill.name, editorContent, activeSkill.scope || scope, teamSlug
        )

        // Show validation results from post-save validation
        if (result.validation?.issues && result.validation.issues.length > 0) {
          setValidationResults(result.validation.issues)
          setValidationVisible(true)
          const acks = result.acknowledged_risks || []
          const hasUnackedCritical = result.validation.issues.some((i: any) =>
            i.severity === 'critical' && !acks.some((a: any) => a.message === i.message && a.type === i.type)
          )
          setValidationBlocked(hasUnackedCritical)
        } else {
          // Clean validation or no issues
          setValidationResults(null)
          setValidationVisible(false)
          setValidationBlocked(false)
        }
        // Update local validation status + acknowledged_risks
        if (result.validation_status) {
          setActiveSkill({ ...activeSkill, validation_status: result.validation_status, raw_file: editorContent, acknowledged_risks: result.acknowledged_risks || activeSkill.acknowledged_risks })
        }
      } else {
        // Saving an auxiliary file
        const url = buildSkillFileUrl(activeSkill.name, currentFilePath, currentFilename, activeSkill.scope)
        const res = await teamFetch(
          url,
          {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              content: editorContent,
              is_executable: false // TODO: detect from file extension or UI
            })
          },
          teamSlug
        )
        if (!res.ok) {
          const errData = await res.json().catch(() => ({}))
          throw new Error(errData.error || 'Failed to save file')
        }
      }
      // Discard stale completion if user switched files during save
      if (saveIdRef.current !== currentSaveId) return
      setEditorSuccess(true)
      setLoadedContent(savedContent) // Reset dirty tracking after successful save
      const t2 = setTimeout(() => { setEditorSuccess(false); timersRef.current.delete(t2) }, 2000)
      timersRef.current.add(t2)
      loadSkills()
      // Refresh skill data — isolated so save success isn't masked by refresh failure
      try {
        const refreshed = await fetchSkillContent(activeSkill.name, activeSkill.scope || scope, teamSlug)
        if (saveIdRef.current === currentSaveId) {
          setActiveSkill(refreshed)
        }
      } catch {
        // Refresh failed but save succeeded — don't show error
      }
    } catch (err: any) {
      if (saveIdRef.current === currentSaveId) {
        setEditorError(err.message)
      }
    } finally {
      if (saveIdRef.current === currentSaveId) {
        setEditorSaving(false)
      }
    }
  }

  const handleValidateSkill = async () => {
    if (!activeSkill) return
    // Validation runs against server-saved content — warn if editor has unsaved changes
    if (editorContent !== loadedContent) {
      if (!window.confirm('You have unsaved changes. Validation runs against the last saved version. Continue anyway?')) return
    }
    setValidating(true)
    setValidationResults(null)
    setValidationVisible(false)
    setValidationBlocked(false)
    try {
      const params = (activeSkill.scope || scope) ? `?scope=${activeSkill.scope || scope}` : ''
      const res = await teamFetch(
        `/api/skills/${encodeURIComponent(activeSkill.name)}/validate${params}`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' } },
        teamSlug
      )
      if (!res.ok) {
        throw new Error('Validation request failed')
      }
      const data = await res.json()
      if (data.status === 'skipped') {
        setEditorError('Validation unavailable — no AI provider configured. Skills cannot be used without validation.')
        return
      }
      // Update local validation status from persisted result
      if (data.validation_status && activeSkill) {
        setActiveSkill({ ...activeSkill, validation_status: data.validation_status })
      }
      if (data.validation && data.validation.issues && data.validation.issues.length > 0) {
        setValidationResults(data.validation.issues)
        setValidationVisible(true)
        const hasCritical = data.validation.issues.some((i: any) => i.severity === 'critical')
        setValidationBlocked(hasCritical)
      } else {
        setValidationResults([])
        setValidationVisible(true)
        // Auto-hide "no issues" after 3 seconds
        const t3 = setTimeout(() => { setValidationVisible(false); timersRef.current.delete(t3) }, 3000)
        timersRef.current.add(t3)
      }
      // Refresh the skills list to show updated badge
      loadSkills()
    } catch (err: any) {
      setEditorError('Validation failed: ' + err.message)
    } finally {
      setValidating(false)
    }
  }

  const handleApplyFix = (suggestion: { old_content: string; new_content: string }) => {
    if (!suggestion.old_content) return // old_content required; new_content can be empty (deletion)
    // NOTE: We intentionally use replace() (first occurrence only), NOT replaceAll().
    // The validator generates old_content snippets that are meant to be unique within
    // the skill content. Using replaceAll() would be more dangerous — if a snippet
    // accidentally matches multiple locations, it would blindly change all of them.
    const updated = editorContent.replace(suggestion.old_content, suggestion.new_content ?? '')
    if (updated !== editorContent) {
      setEditorContent(updated)
      // Remove the applied issue from the list
      if (validationResults) {
        setValidationResults(validationResults.filter(
          (issue) => !(issue.suggestion && issue.suggestion.old_content === suggestion.old_content)
        ))
      }
    } else {
      setEditorError('Could not apply fix — content mismatch')
    }
  }

  const handleAcknowledgeRisk = async (issue: any) => {
    if (!activeSkill) return
    const issueKey = `${issue.type}:${issue.message}`
    setAcknowledging(issueKey)
    try {
      const params = (activeSkill.scope || scope) ? `?scope=${activeSkill.scope || scope}` : ''
      const res = await teamFetch(
        `/api/skills/${encodeURIComponent(activeSkill.name)}/acknowledge${params}`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ message: issue.message, type: issue.type })
        },
        teamSlug
      )
      if (!res.ok) {
        const errData = await res.json().catch(() => ({}))
        setEditorError(errData.error || 'Failed to acknowledge risk')
        return
      }
      const data = await res.json()

      // Add the new ack to activeSkill.acknowledged_risks for immediate UI update
      const existingAcks = activeSkill.acknowledged_risks || []
      const newAck = data.acknowledgment || { message: issue.message, type: issue.type, acknowledged_by_email: '', acknowledged_at: new Date().toISOString() }
      const updatedAcks = [...existingAcks, newAck]

      // Check if there are still unacknowledged critical issues
      if (validationResults) {
        const hasUnackedCritical = validationResults.some((i: any) =>
          i.severity === 'critical' && !updatedAcks.some((a: any) => a.message === i.message && a.type === i.type)
        )
        setValidationBlocked(hasUnackedCritical)
      }

      // Update local state with new ack and validation status
      setActiveSkill({
        ...activeSkill,
        validation_status: data.validation_status || activeSkill.validation_status,
        acknowledged_risks: updatedAcks
      })

      // Refresh the skills list to show updated badge
      loadSkills()
    } catch (err: any) {
      setEditorError('Failed to acknowledge risk: ' + err.message)
    } finally {
      setAcknowledging(null)
    }
  }

  const handleCreateSkill = async () => {
    setCreating(true)
    setCreateError(null)
    try {
      await createSkillApi(newSkillName, scope, teamSlug)
      setShowCreate(false)
      setNewSkillName('')
      await loadSkills()
      handleOpenSkill(newSkillName, 'edit', scope)
    } catch (err: any) {
      setCreateError(err.message)
    } finally {
      setCreating(false)
    }
  }

  const handleDeleteSkill = async (name: string) => {
    try {
      await deleteSkillApi(name, deleteScope || scope, teamSlug)
      setDeleteConfirm(null)
      setDeleteScope(undefined)
      if (activeSkill?.name === name) {
        setActiveSkill(null)
        setEditorMode(null)
      }
      loadSkills()
    } catch (err: any) {
      setSkillsError(err.message)
    }
  }

  const closeEditor = () => {
    if (editorContent !== loadedContent) {
      if (!window.confirm('You have unsaved changes. Discard?')) return
    }
    setActiveSkill(null)
    setEditorMode(null)
    setEditorContent('')
    setLoadedContent('')
    setEditorError(null)
    setEditorSuccess(false)
    setCurrentFilePath('')
    setCurrentFilename('SKILL.md')
    setShowAddFile(false)
    setPendingDeleteFile(null)
    setAddFileError(null)
  }

  // Group skills by scope for platform mode display
  const teamSkills = skillsList.filter(s => s.scope === 'team')
  const orgSkills = skillsList.filter(s => s.scope === 'org')
  const platformSkills = skillsList.filter(s => s.scope === 'platform')
  const customSkills = skillsList.filter(s => s.scope !== 'platform')

  // Full-screen editor mode
  if (editorMode) {
    const allPaths = [...new Set((activeSkill?.files || []).map(f => f.path || ''))]
    const fileGroups = allPaths.filter(g => g !== '').sort().concat(allPaths.includes('') ? [''] : [])

    const renderFileItem = (f: { path: string; filename: string; size?: number; is_executable?: boolean }, isActive: boolean) => {
      const isPendingDelete = pendingDeleteFile && pendingDeleteFile.path === f.path && pendingDeleteFile.filename === f.filename
      if (isPendingDelete) {
        return (
          <div key={`${f.path}/${f.filename}`} className="ml-6 flex items-center gap-2 px-2 py-0.5 rounded bg-red-900/30 text-red-300 text-xs">
            <span>Delete {f.filename}?</span>
            <button onClick={() => handleDeleteFile(f.path, f.filename)} className="px-2 py-0.5 rounded bg-red-600 hover:bg-red-500 text-white text-[10px]">Yes</button>
            <button onClick={() => setPendingDeleteFile(null)} className="px-2 py-0.5 rounded hover:bg-gray-700 text-[10px]">Cancel</button>
          </div>
        )
      }
      return (
        <div
          key={`${f.path}/${f.filename}`}
          onClick={() => switchToFile(f.path, f.filename)}
          className={`group flex items-center justify-between px-2 py-1 rounded cursor-pointer text-sm ${isActive ? 'bg-primary text-white' : 'hover:bg-gray-800/60'}`}
        >
          <div className="flex items-center gap-1.5 min-w-0">
            <FileText size={14} className="flex-shrink-0" />
            <span className="truncate">{f.filename}</span>
            {f.is_executable && <span className="text-[10px] opacity-70">⚡</span>}
          </div>
          <div className="flex items-center gap-1 text-[10px] opacity-60">
            {f.size && <span>{Math.round(f.size / 1024)}k</span>}
            {activeSkill?.editable && (
              <button
                onClick={(e) => { e.stopPropagation(); setPendingDeleteFile({ path: f.path, filename: f.filename }) }}
                className="opacity-0 group-hover:opacity-100 p-0.5 hover:text-red-400"
                title="Delete file"
              >
                <Trash2 size={12} />
              </button>
            )}
          </div>
        </div>
      )
    }

    return (
      <div className="flex flex-col h-full" style={{ margin: '-24px', height: 'calc(100% + 48px)' }}>
        {/* Editor header */}
        <div className="flex items-center justify-between px-4 py-2.5 border-b flex-shrink-0" style={sectionBorderStyle}>
          <div className="flex items-center gap-3 min-w-0">
            <button
              onClick={closeEditor}
              className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30 flex-shrink-0"
              style={{ color: 'var(--text-muted)' }}
            >
              <X size={18} />
            </button>
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                  {activeSkill?.name}
                </span>
                {activeSkill?.scope && <ScopeBadge scope={activeSkill.scope} />}
                {!activeSkill?.scope && (
                  <span className="text-xs px-2 py-0.5 rounded flex-shrink-0" style={{
                    background: activeSkill?.source === 'platform' ? 'var(--brand-muted)' : 'rgba(34, 197, 94, 0.15)',
                    color: activeSkill?.source === 'platform' ? 'var(--brand)' : '#22c55e'
                  }}>
                    {activeSkill?.source}
                  </span>
                )}
                {editorMode === 'view' && (
                  <span className="text-xs px-2 py-0.5 rounded flex-shrink-0" style={{ background: 'rgba(100,100,100,0.2)', color: 'var(--text-muted)' }}>
                    read-only
                  </span>
                )}
              </div>
              {/* Breadcrumb for current file */}
              <div className="text-xs mt-0.5 flex items-center gap-1" style={hintStyle}>
                <span>{activeSkill?.name}</span>
                <span>/</span>
                {currentFilePath && <span>{currentFilePath}/</span>}
                <span className="font-medium" style={{ color: 'var(--text-primary)' }}>{currentFilename}</span>
                {currentFilename === 'SKILL.md' && <span className="opacity-50">(main)</span>}
              </div>
            </div>
          </div>

          <div className="flex items-center gap-2 flex-shrink-0">
            {editorSuccess && (
              <span className="flex items-center gap-1 text-green-400 text-sm">
                <Check size={14} /> Saved
              </span>
            )}
            {editorError && (
              <span className="flex items-center gap-1 text-sm" style={{ color: 'var(--danger)' }}>
                <AlertCircle size={14} /> {editorError}
              </span>
            )}
            {editorMode === 'view' && activeSkill?.editable && (
              <button
                onClick={() => setEditorMode('edit')}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-all"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
              >
                <Pencil size={14} /> Edit
              </button>
            )}
            {editorMode === 'edit' && (
              <button
                onClick={handleSaveSkill}
                disabled={editorSaving || !isDirty}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-white text-sm font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50 disabled:cursor-not-allowed"
                style={saveButtonStyle}
                title={!isDirty ? 'No changes to save' : 'Save changes'}
              >
                <Save size={14} />
                {editorSaving ? 'Saving...' : 'Save'}
              </button>
            )}
            {editorMode === 'edit' && (
              <button
                onClick={handleValidateSkill}
                disabled={validating}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-all"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
              >
                <AlertCircle size={14} />
                {validating ? 'Validating...' : 'Validate'}
              </button>
            )}
          </div>
        </div>

        {/* Validation Results Panel */}
        {validationVisible && validationResults !== null && (
          <div className="border-b px-4 py-3" style={{ background: validationBlocked ? 'rgba(239, 68, 68, 0.05)' : 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
            {validationResults.length === 0 ? (
              <span className="flex items-center gap-1.5 text-sm text-green-400">
                <Check size={14} /> No issues found — skill is validated and usable
              </span>
            ) : (
            <div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium" style={{ color: validationBlocked ? '#f87171' : 'var(--text-primary)' }}>
                {validationBlocked ? '⛔ Blocked — ' : ''}Validation Issues ({validationResults.length})
              </span>
              <div className="flex items-center gap-2">
                {!validationBlocked && (
                  <button
                    onClick={() => { setValidationVisible(false); setValidationBlocked(false) }}
                    className="text-xs px-2 py-0.5 rounded"
                    style={{ color: 'var(--text-secondary)' }}
                  >
                    Dismiss
                  </button>
                )}
              </div>
            </div>
            <div className="space-y-2 max-h-48 overflow-y-auto">
              {validationResults.map((issue, idx) => {
                // Check if this critical issue has already been acknowledged
                const acks = activeSkill?.acknowledged_risks || []
                const matchingAck = issue.severity === 'critical'
                  ? acks.find((a: any) => a.message === issue.message && a.type === issue.type)
                  : null

                return (
                <div key={idx} className="flex items-start gap-2 text-sm rounded-lg p-2" style={{ background: 'var(--bg-primary)', border: `1px solid ${issue.severity === 'critical' && !matchingAck ? 'rgba(239, 68, 68, 0.3)' : 'var(--border-color)'}` }}>
                  <span className={`shrink-0 px-1.5 py-0.5 rounded text-xs font-medium ${
                    issue.severity === 'critical' && !matchingAck ? 'bg-red-500/20 text-red-400' :
                    issue.severity === 'critical' && matchingAck ? 'bg-green-500/20 text-green-400' :
                    issue.severity === 'warning' ? 'bg-yellow-500/20 text-yellow-400' :
                    'bg-blue-500/20 text-blue-400'
                  }`}>
                    {issue.severity === 'critical' && matchingAck ? 'acknowledged' : issue.severity}
                  </span>
                  <div className="flex-1 min-w-0">
                    <p style={{ color: 'var(--text-primary)' }}>{issue.message}</p>
                    <div className="flex items-center gap-2 mt-1">
                      {issue.suggestion && !matchingAck && (
                        <button
                          onClick={() => handleApplyFix(issue.suggestion)}
                          className="text-xs px-2 py-1 rounded transition-colors bg-primary/80 hover:bg-primary text-white font-medium"
                        >
                          Apply Fix
                        </button>
                      )}
                      {issue.severity === 'critical' && !matchingAck && (
                        <button
                          onClick={() => handleAcknowledgeRisk(issue)}
                          disabled={acknowledging === `${issue.type}:${issue.message}`}
                          className="text-xs px-2 py-0.5 rounded transition-colors bg-orange-600/80 hover:bg-orange-600 text-white disabled:opacity-50 disabled:cursor-not-allowed"
                          title="Accept this risk — you understand the security implications"
                        >
                          {acknowledging === `${issue.type}:${issue.message}` ? 'Acknowledging...' : 'Acknowledge Risk'}
                        </button>
                      )}
                      {matchingAck && (
                        <span className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
                          Acknowledged by {matchingAck.acknowledged_by_email || 'unknown'} on {new Date(matchingAck.acknowledged_at).toLocaleDateString()}
                        </span>
                      )}
                    </div>
                  </div>
                  <span className="text-xs shrink-0" style={{ color: 'var(--text-tertiary)' }}>
                    {issue.type}
                  </span>
                </div>
                )
              })}
            </div>
            </div>
            )}
          </div>
        )}

        {/* Sidebar + Editor split */}
        <div className="flex flex-1 overflow-hidden">
           {/* LEFT FILE SIDEBAR */}
           <div className="w-56 border-r flex flex-col shrink-0" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
             {/* Sidebar header */}
             <div className="px-3 py-2 text-xs font-semibold border-b flex items-center justify-between flex-shrink-0" style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}>
               <div className="flex items-center gap-1.5">
                 <FolderOpen size={14} />
                 <span>Files</span>
               </div>
               {activeSkill?.editable && (
                 <button
                    onClick={() => { setShowAddFile(true); setAddFileError(null); setNewFileName('') }}
                   className="text-[11px] px-2 py-0.5 rounded bg-emerald-700 hover:bg-emerald-600 text-white flex items-center gap-1"
                 >
                   <Plus size={12} /> Add
                 </button>
               )}
             </div>

             {/* File tree */}
             <div className="flex-1 overflow-y-auto p-1 text-sm space-y-0.5" style={{ color: 'var(--text-primary)' }}>
               {/* SKILL.md - always first */}
               <div
                 onClick={async () => {
                   if (currentFilename === 'SKILL.md' && currentFilePath === '') return // already active
                   if (editorContent !== loadedContent) {
                     if (!window.confirm('You have unsaved changes. Discard and switch files?')) return
                   }
                   setCurrentFilePath('')
                   setCurrentFilename('SKILL.md')
                   // Re-fetch to avoid stale content if another user edited SKILL.md
                   if (activeSkill) {
                     try {
                       const refreshed = await fetchSkillContent(activeSkill.name, activeSkill.scope || scope, teamSlug)
                       setActiveSkill(refreshed)
                       setEditorContent(refreshed.raw_file || '')
                       setLoadedContent(refreshed.raw_file || '')
                     } catch {
                       // Fallback to cached value if fetch fails
                       setEditorContent(activeSkill.raw_file || '')
                       setLoadedContent(activeSkill.raw_file || '')
                     }
                   }
                 }}
                 className={`group flex items-center gap-1.5 px-2 py-1 rounded cursor-pointer ${currentFilename === 'SKILL.md' ? 'bg-primary text-white' : 'hover:bg-gray-800/60'}`}
               >
                 <FileText size={14} />
                 <span className="font-medium">SKILL.md</span>
                 <span className="ml-auto text-[10px] opacity-60">main</span>
               </div>

               {/* Grouped auxiliary files */}
               {fileGroups.map(group => {
                 const gFiles = (activeSkill?.files || []).filter(f => (f.path || '') === group)
                 if (gFiles.length === 0) return null
                 return (
                   <div key={group} className="mt-1">
                     {group && (
                       <div className="flex items-center gap-1 px-2 py-0.5 text-[11px] text-gray-400 font-medium">
                         <ChevronDown size={12} /> {group}/
                       </div>
                     )}
                     <div className={group ? 'ml-3' : ''}>
                       {gFiles.map(f => renderFileItem(f, currentFilePath === f.path && currentFilename === f.filename))}
                     </div>
                   </div>
                 )
               })}

               {/* Empty state */}
               {(!activeSkill?.files || activeSkill.files.length === 0) && (
                 <div className="px-2 py-3 text-[11px] text-gray-400 italic">
                   No extra files yet.<br />Use "+ Add" to create scripts/, references/, etc.
                 </div>
               )}
             </div>
           </div>

          {/* MAIN EDITOR AREA */}
          <div className="flex-1 flex flex-col overflow-hidden">
            {editorLoading ? (
              <div className="flex items-center justify-center h-full">
                <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
              </div>
            ) : (
              <CodeMirror
                value={editorContent}
                onChange={editorMode === 'edit' ? (value: string) => setEditorContent(value) : undefined}
                readOnly={editorMode === 'view'}
                height="100%"
                className="h-full"
                extensions={[
                  getLanguageExtension(currentFilename),
                  search({ scrollToMatch: (range) => EditorView.scrollIntoView(range, { y: 'center', yMargin: 100 }) }),
                  highlightSelectionMatches(),
                  keymap.of(searchKeymap),
                ]}
                theme={theme === 'dark' ? 'dark' : 'light'}
                basicSetup={{
                  lineNumbers: true,
                  highlightActiveLineGutter: true,
                  highlightActiveLine: true,
                  foldGutter: true,
                }}
              />
            )}
           </div>
         </div>

         {/* Add New File Modal — rendered inside editor return so it appears when editing */}
         {showAddFile && (
           <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }} onClick={() => { setShowAddFile(false); setAddFileError(null) }}>
             <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
               style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}
               onClick={(e) => e.stopPropagation()}>
               <div className="flex items-center justify-between mb-4">
                 <h3 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Add New File</h3>
                 <button onClick={() => { setShowAddFile(false); setAddFileError(null) }} className="p-1.5 rounded-lg hover:bg-gray-600/30" style={{ color: 'var(--text-muted)' }}>
                   <X size={18} />
                 </button>
               </div>

                <div className="space-y-4">
                  {/* Full relative path */}
                  <div>
                    <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
                      File Path
                    </label>
                    <input
                      type="text"
                      value={newFileName}
                      onChange={(e) => setNewFileName(e.target.value)}
                      onKeyDown={(e) => { if (e.key === 'Enter' && newFileName.trim()) handleAddFile() }}
                      placeholder="scripts/deploy.sh"
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                      autoFocus
                    />
                    <p className="text-xs mt-1" style={hintStyle}>
                      Full relative path including filename (e.g. scripts/deploy.sh, references/api.md, config.json).
                    </p>
                  </div>

                  {addFileError && (
                   <div className="flex items-center gap-2 p-2 rounded-lg text-sm"
                     style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
                     <AlertCircle size={14} /> {addFileError}
                   </div>
                 )}
               </div>

               <div className="flex justify-end gap-3 mt-6">
                 <button
                   onClick={() => { setShowAddFile(false); setAddFileError(null) }}
                   className="px-4 py-2 rounded-lg text-sm font-medium"
                   style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
                 >
                   Cancel
                 </button>
                 <button
                   onClick={handleAddFile}
                   disabled={!newFileName.trim()}
                   className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                   style={saveButtonStyle}
                 >
                   <Plus size={14} />
                   Add File
                 </button>
               </div>
             </div>
           </div>
         )}
       </div>
     )
   }

  return (
    <div className="w-full space-y-6">
      {/* Master Toggle — only in personal mode settings */}
      {showConfig && (
        <div className="flex items-center justify-between">
          <div>
            <label className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Enable Skills
            </label>
            <p className="text-xs mt-0.5" style={hintStyle}>
              Load custom skills that extend the agent with specialized behaviors and tools. Default: enabled.
            </p>
          </div>
          <button
            onClick={() => setForm({ ...form, enabled: !form.enabled })}
            className="relative w-11 h-6 rounded-full transition-colors"
            style={{
              background: form.enabled ? 'var(--brand)' : 'var(--bg-tertiary)',
              border: `1px solid ${form.enabled ? 'var(--brand)' : 'var(--border-color)'}`
            }}
          >
            <span
              className="absolute top-0.5 left-0.5 w-4 h-4 rounded-full transition-transform bg-white"
              style={{ transform: form.enabled ? 'translateX(20px)' : 'translateX(0)' }}
            />
          </button>
        </div>
      )}

      {(showConfig ? form.enabled : true) && (
        <>
          {/* Skills List */}
          <div className={showConfig ? 'pt-4 border-t' : ''} style={showConfig ? sectionBorderStyle : undefined}>
            <div className="flex items-center justify-between mb-3">
              <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                {scope === 'team' ? 'Team Skills' : scope === 'org' ? 'Organization Skills' : isPlatform ? 'Skills' : 'Installed Skills'}
                {!skillsLoading && (
                  <span className="ml-2 text-xs font-normal" style={hintStyle}>
                    {skillsList.filter(s => s.eligible).length} eligible, {skillsList.length} total
                  </span>
                )}
              </h4>
              {effectiveCanManage && (
                <button
                  onClick={() => { setShowCreate(true); setNewSkillName(''); setCreateError(null) }}
                  className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95"
                  style={{ background: 'var(--brand)', color: '#fff' }}
                >
                  <Plus size={14} /> New Skill
                </button>
              )}
            </div>

            {skillsLoading && (
              <div className="flex items-center gap-2 py-4">
                <Loader2 size={16} className="animate-spin" style={{ color: 'var(--brand)' }} />
                <span className="text-sm" style={hintStyle}>Loading skills...</span>
              </div>
            )}

            {skillsError && (
              <div className="flex items-center gap-2 p-3 rounded-lg text-sm mb-3"
                style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
                <AlertCircle size={14} /> {skillsError}
              </div>
            )}

            {/* Platform mode: scoped sections */}
            {isPlatform && !scope && (
              <>
                {/* Team Skills */}
                {teamSkills.length > 0 && (
                  <SkillSection
                    label="Team"
                    skills={teamSkills}
                    canManage={isTeamAdmin}
                    onView={(s) => handleOpenSkill(s.name, 'view', 'team')}
                    onEdit={(s) => handleOpenSkill(s.name, 'edit', 'team')}
                    onDelete={(s) => { setDeleteConfirm(s.name); setDeleteScope('team') }}
                  />
                )}

                {/* Org Skills */}
                {orgSkills.length > 0 && (
                  <SkillSection
                    label="Organization (inherited)"
                    skills={orgSkills}
                    canManage={isOrgAdmin}
                    onView={(s) => handleOpenSkill(s.name, 'view', 'org')}
                    onEdit={(s) => handleOpenSkill(s.name, 'edit', 'org')}
                    onDelete={(s) => { setDeleteConfirm(s.name); setDeleteScope('org') }}
                  />
                )}

                {/* Platform Skills */}
                {platformSkills.length > 0 && (
                  <SkillSection
                    label="Platform (inherited)"
                    skills={platformSkills}
                    canManage={false}
                    onView={(s) => handleOpenSkill(s.name, 'view', 'platform')}
                  />
                )}
              </>
            )}

            {/* Scoped or personal mode: Platform (inherited) / Custom grouping */}
            {(isPlatform ? !!scope : true) && (
              <>
                {platformSkills.length > 0 && (
                  <div className="mb-4">
                    <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={hintStyle}>
                      Platform (inherited)
                    </div>
                    <div className="space-y-1">
                      {platformSkills.map(skill => (
                        <SkillRow
                          key={skill.name}
                          skill={skill}
                          onView={() => handleOpenSkill(skill.name, 'view', isPlatform ? 'platform' : undefined)}
                        />
                      ))}
                    </div>
                  </div>
                )}

                {customSkills.length > 0 && (
                  <div className="mb-4">
                    <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={hintStyle}>
                      Custom
                    </div>
                    <div className="space-y-1">
                      {customSkills.map(skill => (
                        <SkillRow
                          key={skill.name}
                          skill={skill}
                          onView={() => handleOpenSkill(skill.name, 'view', scope)}
                          onEdit={effectiveCanManage && skill.editable ? () => handleOpenSkill(skill.name, 'edit', scope) : undefined}
                          onDelete={effectiveCanManage && skill.editable ? () => { setDeleteConfirm(skill.name); setDeleteScope(scope) } : undefined}
                        />
                      ))}
                    </div>
                  </div>
                )}
              </>
            )}

            {!skillsLoading && skillsList.length === 0 && (
              <p className="text-sm py-4" style={hintStyle}>
                {scope === 'team'
                  ? 'No team skills yet. Click "New Skill" to create one.'
                  : scope === 'org'
                    ? 'No organization skills yet. Click "New Skill" to create one.'
                    : 'No skills found. Click "New Skill" to create one, or install from ClawHub via CLI: astonish skills install <slug>'
                }
              </p>
            )}
          </div>

          {/* Configuration — personal mode only */}
          {showConfig && (
            <>
              <div className="pt-4 border-t" style={sectionBorderStyle}>
                <h4 className="text-sm font-medium mb-3" style={{ color: 'var(--text-primary)' }}>
                  Configuration
                </h4>
                <div className="space-y-4">
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      User Skills Directory
                    </label>
                    <input
                      type="text"
                      value={form.user_dir}
                      onChange={(e) => setForm({ ...form, user_dir: e.target.value })}
                      placeholder="~/.config/astonish/skills/ (default)"
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                    <p className="text-xs mt-1" style={hintStyle}>
                      Directory for user-defined skill files. Default: ~/.config/astonish/skills/
                    </p>
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Extra Directories
                    </label>
                    <input
                      type="text"
                      value={extraDirsText}
                      onChange={(e) => setExtraDirsText(e.target.value)}
                      placeholder="No extra directories"
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                    <p className="text-xs mt-1" style={hintStyle}>
                      Comma-separated additional directories to search for skills.
                    </p>
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-2" style={labelStyle}>
                      Allowlist
                    </label>
                    <input
                      type="text"
                      value={allowlistText}
                      onChange={(e) => setAllowlistText(e.target.value)}
                      placeholder="All eligible skills (default)"
                      className={inputClass + ' font-mono'}
                      style={inputStyle}
                    />
                    <p className="text-xs mt-1" style={hintStyle}>
                      Comma-separated skill names to allow. Leave empty to load all eligible skills.
                    </p>
                  </div>
                </div>
              </div>

              {/* Save Config */}
              <div className="flex items-center gap-3">
                <button
                  onClick={handleSaveConfig}
                  disabled={saving}
                  className="flex items-center gap-2 px-4 py-2 rounded-lg text-white font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                  style={saveButtonStyle}
                >
                  <Save size={16} />
                  {saving ? 'Saving...' : 'Save Configuration'}
                </button>
                {saveSuccess && (
                  <span className="flex items-center gap-1 text-green-400 text-sm">
                    <Check size={16} /> Saved
                  </span>
                )}
                {error && (
                  <span className="flex items-center gap-1 text-sm" style={{ color: 'var(--danger)' }}>
                    <AlertCircle size={16} /> {error}
                  </span>
                )}
              </div>
            </>
          )}
        </>
      )}

      {/* Create Skill Modal */}
      {showCreate && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-md p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Create New Skill</h3>
              <button onClick={() => setShowCreate(false)} className="p-1.5 rounded-lg hover:bg-gray-600/30" style={{ color: 'var(--text-muted)' }}>
                <X size={18} />
              </button>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>Skill Name</label>
                <input
                  type="text"
                  value={newSkillName}
                  onChange={(e) => setNewSkillName(e.target.value.toLowerCase().replace(/[^a-z0-9-_]/g, ''))}
                  placeholder="my-custom-skill"
                  className={inputClass + ' font-mono'}
                  style={inputStyle}
                  autoFocus
                  onKeyDown={(e) => { if (e.key === 'Enter' && newSkillName.trim()) handleCreateSkill() }}
                />
                <p className="text-xs mt-1" style={hintStyle}>
                  Lowercase letters, numbers, hyphens, and underscores only.
                </p>
              </div>
              {createError && (
                <div className="flex items-center gap-2 p-2 rounded-lg text-sm"
                  style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
                  <AlertCircle size={14} /> {createError}
                </div>
              )}
              <div className="flex justify-end gap-3">
                <button
                  onClick={() => setShowCreate(false)}
                  className="px-4 py-2 rounded-lg text-sm font-medium"
                  style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
                >
                  Cancel
                </button>
                <button
                  onClick={handleCreateSkill}
                  disabled={creating || !newSkillName.trim()}
                  className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                  style={saveButtonStyle}
                >
                  {creating ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
                  {creating ? 'Creating...' : 'Create'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Delete Confirmation Modal */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h3 className="text-lg font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Delete Skill</h3>
            <p className="text-sm mb-4" style={hintStyle}>
              Are you sure you want to delete <strong style={{ color: 'var(--text-primary)' }}>{deleteConfirm}</strong>? This cannot be undone.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => { setDeleteConfirm(null); setDeleteScope(undefined) }}
                className="px-4 py-2 rounded-lg text-sm font-medium"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
              >
                Cancel
              </button>
              <button
                onClick={() => handleDeleteSkill(deleteConfirm)}
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

// --- Sub-components ---

// SkillSection renders a labeled group of skills
function SkillSection({ label, skills, canManage, onView, onEdit, onDelete }: {
  label: string
  skills: Skill[]
  canManage: boolean
  onView?: (s: Skill) => void
  onEdit?: (s: Skill) => void
  onDelete?: (s: Skill) => void
}) {
  return (
    <div className="mb-4">
      <div className="text-xs font-medium mb-2 uppercase tracking-wider" style={hintStyle}>
        {label}
      </div>
      <div className="space-y-1">
        {skills.map(skill => (
          <SkillRow
            key={skill.name}
            skill={skill}
            showScope
            onView={onView ? () => onView(skill) : undefined}
            onEdit={canManage && skill.editable && onEdit ? () => onEdit(skill) : undefined}
            onDelete={canManage && skill.editable && onDelete ? () => onDelete(skill) : undefined}
          />
        ))}
      </div>
    </div>
  )
}

// Skill row component
interface SkillRowProps {
  skill: Skill
  showScope?: boolean
  onView?: () => void
  onEdit?: () => void
  onDelete?: () => void
}

function SkillRow({ skill, showScope, onView, onEdit, onDelete }: SkillRowProps) {
  const isBlocked = skill.validation_status === 'blocked' || skill.validation_status === 'unknown'
  const isValidated = skill.validation_status === 'clean' || skill.validation_status === 'warnings' || skill.validation_status === 'acknowledged'

  return (
    <div
      className="flex items-center gap-3 px-3 py-2 rounded-lg transition-colors group"
      style={{ background: 'var(--bg-secondary)', border: '1px solid transparent' }}
      onMouseEnter={(e) => { (e.currentTarget as HTMLDivElement).style.borderColor = 'var(--border-color)' }}
      onMouseLeave={(e) => { (e.currentTarget as HTMLDivElement).style.borderColor = 'transparent' }}
    >
      {/* Status icon */}
      <div className="flex-shrink-0">
        {skill.eligible ? (
          <CheckCircle size={16} style={{ color: '#22c55e' }} />
        ) : (
          <XCircle size={16} style={{ color: '#ef4444' }} />
        )}
      </div>

      {/* Name and description */}
      <div className="flex-1 min-w-0">
         <div className="flex items-center gap-2">
           <span className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>
             {skill.name}
           </span>
           {showScope && skill.scope && <ScopeBadge scope={skill.scope} />}
           {skill.has_directory && (
             <span className="text-[10px] px-1.5 py-0.5 rounded bg-blue-900/40 text-blue-300" title="This skill contains multiple files (scripts, references, etc.)">
               multi-file
             </span>
           )}
           {/* Validation status badge */}
           {isBlocked && (
             <span className="text-[10px] px-1.5 py-0.5 rounded bg-red-900/40 text-red-300 flex items-center gap-1"
               title={skill.validation_status === 'unknown' ? 'Not validated — must be validated before use' : 'Blocked — critical security issues need review'}>
               <ShieldAlert size={10} />
               {skill.validation_status === 'unknown' ? 'unvalidated' : 'blocked'}
             </span>
           )}
           {isValidated && (
             <span className="text-[10px] px-1.5 py-0.5 rounded bg-green-900/40 text-green-300 flex items-center gap-1"
               title={skill.validation_status === 'acknowledged' ? 'Validated — risks acknowledged' : 'Validated'}>
               <ShieldCheck size={10} />
               {skill.validation_status === 'acknowledged' ? 'acknowledged' : 'validated'}
             </span>
           )}
           {skill.require_bins?.length && skill.require_bins.length > 0 && !skill.eligible && (
             <span className="text-xs px-1.5 py-0.5 rounded flex-shrink-0"
               style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
               missing: {(skill.missing || []).join(', ')}
             </span>
           )}
         </div>
        <div className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>
          {skill.description}
        </div>
      </div>

      {/* Actions */}
      <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity flex-shrink-0">
        {onView && (
          <button
            onClick={onView}
            className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30"
            style={{ color: 'var(--text-muted)' }}
            title="View"
          >
            <Eye size={14} />
          </button>
        )}
        {onEdit && (
          <button
            onClick={onEdit}
            className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30"
            style={{ color: 'var(--text-muted)' }}
            title="Edit"
          >
            <Pencil size={14} />
          </button>
        )}
        {onDelete && (
          <button
            onClick={onDelete}
            className="p-1.5 rounded-lg transition-colors hover:bg-red-600/20"
            style={{ color: 'var(--text-muted)' }}
            title="Delete"
          >
            <Trash2 size={14} />
          </button>
        )}
        {!onEdit && !onDelete && (
          <ChevronRight size={14} style={{ color: 'var(--text-muted)' }} />
        )}
      </div>
    </div>
  )
}
