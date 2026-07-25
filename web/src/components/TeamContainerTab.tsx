import React, { useState, useEffect, useCallback, useRef, lazy, Suspense } from 'react'
import { Box, Play, Save, RotateCcw, Trash2, Loader2, AlertCircle, CheckCircle2, Info } from 'lucide-react'
import CodeMirror from '@uiw/react-codemirror'
import { StreamLanguage } from '@codemirror/language'
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'
import {
  fetchTeamTemplateStatus, createTeamTemplate, saveTeamTemplate,
  restoreTeamTemplate, deleteTeamTemplate, startTeamTemplate,
  setTeamTemplateImage, buildTeamImage,
  fetchTeamDockerfile, saveTeamDockerfile,
  type TeamTemplateStatus,
} from '../api/sandbox'

const TeamContainerTerminal = lazy(() => import('./TeamContainerTerminal'))

interface TeamContainerTabProps {
  teamSlug: string
  theme: 'dark' | 'light'
  canManage: boolean
}

const btnBase = 'flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md transition-colors disabled:opacity-50 disabled:cursor-not-allowed'

/**
 * TeamContainerTab manages the per-team sandbox template container.
 * Allows admins to create, customize (via terminal), save, restore, and delete
 * the team template that fleet sessions will use.
 */
export default function TeamContainerTab({ teamSlug, theme, canManage }: TeamContainerTabProps) {
  const [status, setStatus] = useState<TeamTemplateStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [showTerminal, setShowTerminal] = useState(false)

  // OpenShell image state
  const [imageInput, setImageInput] = useState('')
  const [savingImage, setSavingImage] = useState(false)
  const [dockerfileBody, setDockerfileBody] = useState('')
  const [savedDockerfileBody, setSavedDockerfileBody] = useState('')
  const [savingDockerfile, setSavingDockerfile] = useState(false)
  const [buildingImage, setBuildingImage] = useState(false)
  const [buildLog, setBuildLog] = useState<string[]>([])
  const buildAbortRef = useRef<(() => void) | null>(null)

  const loadStatus = useCallback(async () => {
    try {
      setError('')
      const s = await fetchTeamTemplateStatus(teamSlug)
      setStatus(s)
      // Populate image input for OpenShell backend
      if (s.backend === 'openshell') {
        setImageInput(s.sandboxImage || '')
        // Load the saved Dockerfile body
        fetchTeamDockerfile(teamSlug).then(body => {
          setDockerfileBody(body)
          setSavedDockerfileBody(body)
        })
      }
      // Auto-show terminal if container is running
      if (s.exists && s.running) {
        setShowTerminal(true)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load template status')
    } finally {
      setLoading(false)
    }
  }, [teamSlug])

  useEffect(() => {
    loadStatus()
  }, [loadStatus])

  // Clear success message after 3 seconds
  useEffect(() => {
    if (!success) return
    const t = setTimeout(() => setSuccess(''), 3000)
    return () => clearTimeout(t)
  }, [success])

  // Poll for status while container exists but is not yet running
  // (e.g. pod in Pending/Creating phase after Create or Start).
  // Stops once running or after ~30s timeout.
  const pollCountRef = useRef(0)
  useEffect(() => {
    if (!showTerminal || !status?.exists || status.running) {
      pollCountRef.current = 0
      return
    }
    // Max 20 polls × 1.5s = 30s
    if (pollCountRef.current >= 20) return
    const id = setInterval(() => {
      pollCountRef.current += 1
      loadStatus()
      if (pollCountRef.current >= 20) clearInterval(id)
    }, 1500)
    return () => clearInterval(id)
  }, [showTerminal, status?.exists, status?.running, loadStatus])

  const handleCreate = async () => {
    setActionLoading('create')
    setError('')
    try {
      await createTeamTemplate(teamSlug)
      setSuccess('Template container created and started')
      await loadStatus()
      setShowTerminal(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create template')
    } finally {
      setActionLoading(null)
    }
  }

  const handleStart = async () => {
    setActionLoading('start')
    setError('')
    try {
      await startTeamTemplate(teamSlug)
      setSuccess('Container started')
      await loadStatus()
      setShowTerminal(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start container')
    } finally {
      setActionLoading(null)
    }
  }

  const handleSave = async () => {
    setActionLoading('save')
    setError('')
    try {
      await saveTeamTemplate(teamSlug)
      setSuccess('Template saved — all new fleet sessions will use this environment')
      await loadStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save template')
    } finally {
      setActionLoading(null)
    }
  }

  const handleRestore = async () => {
    if (!confirm('This will destroy the current container and recreate it from @base. Continue?')) return
    setActionLoading('restore')
    setError('')
    setShowTerminal(false)
    try {
      await restoreTeamTemplate(teamSlug)
      setSuccess('Template restored to base')
      await loadStatus()
      setShowTerminal(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restore template')
    } finally {
      setActionLoading(null)
    }
  }

  const handleDelete = async () => {
    if (!confirm('Delete the team template? Fleet sessions will revert to using @base.')) return
    setActionLoading('delete')
    setError('')
    setShowTerminal(false)
    try {
      await deleteTeamTemplate(teamSlug)
      setSuccess('Template deleted')
      await loadStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete template')
    } finally {
      setActionLoading(null)
    }
  }

  const handleTerminalDisconnect = useCallback(() => {
    // Reload status in case container stopped
    loadStatus()
  }, [loadStatus])

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
        <span className="ml-2 text-sm" style={{ color: 'var(--text-muted)' }}>Loading container status...</span>
      </div>
    )
  }

  // OpenShell backend: show image-setting UI instead of interactive terminal
  if (status?.backend === 'openshell') {
    const handleSaveImage = async () => {
      setError('')
      setSuccess('')
      setSavingImage(true)
      try {
        await setTeamTemplateImage(imageInput.trim(), teamSlug)
        setSuccess(imageInput.trim() ? `Custom image set: ${imageInput.trim()}` : 'Custom image cleared. Using platform default.')
        await loadStatus()
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to set image')
      } finally {
        setSavingImage(false)
      }
    }

    return (
      <div className="flex flex-col h-full gap-4">
        {/* Header */}
        <div>
          <h3 className="text-sm font-semibold flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
            <Box size={16} style={{ color: 'var(--brand)' }} />
            Team Sandbox Image
          </h3>
          <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
            Set a custom container image for this team. New sandboxes will use this image instead of the platform default.
          </p>
        </div>

        {/* Messages */}
        {error && (
          <div className="flex items-center gap-2 p-3 rounded-lg text-xs" style={{
            background: 'rgba(239, 68, 68, 0.1)',
            color: 'var(--danger)',
            border: '1px solid rgba(239, 68, 68, 0.2)',
          }}>
            <AlertCircle size={14} />
            <span>{error}</span>
          </div>
        )}
        {success && (
          <div className="flex items-center gap-2 p-3 rounded-lg text-xs" style={{
            background: 'rgba(34, 197, 94, 0.1)',
            color: '#22c55e',
            border: '1px solid rgba(34, 197, 94, 0.2)',
          }}>
            <CheckCircle2 size={14} />
            <span>{success}</span>
          </div>
        )}

        {/* Current image display */}
        <div className="rounded-xl p-4 space-y-3" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Current Image</span>
            {status.sandboxImage ? (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'color-mix(in oklab, var(--brand) 10%, transparent)', color: 'var(--brand)' }}>
                Custom
              </span>
            ) : (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'rgba(107, 114, 128, 0.1)', color: 'var(--text-muted)' }}>
                Platform Default
              </span>
            )}
          </div>
          <p className="text-xs font-mono break-all" style={{ color: 'var(--text-primary)' }}>
            {status.sandboxImage || 'Using platform default image'}
          </p>
        </div>

        {/* Image input form */}
        {canManage && (
          <div className="rounded-xl p-4 space-y-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h4 className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Set Custom Image</h4>
            <div className="space-y-2">
              <input
                type="text"
                value={imageInput}
                onChange={e => setImageInput(e.target.value)}
                placeholder="ghcr.io/org/custom-sandbox:latest"
                className="w-full px-3 py-2 rounded-lg text-xs outline-none font-mono"
                style={{
                  background: 'var(--bg-tertiary)',
                  border: '1px solid var(--border-color)',
                  color: 'var(--text-primary)',
                }}
              />
              <p className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
                Leave empty to use the platform default. The image must have the OpenShell supervisor installed.
              </p>
            </div>
            <div className="flex items-center justify-end gap-2">
              {imageInput !== (status.sandboxImage || '') && (
                <button
                  onClick={() => setImageInput(status.sandboxImage || '')}
                  className={btnBase}
                  style={{ color: 'var(--text-muted)', border: '1px solid var(--border-color)' }}
                >
                  Reset
                </button>
              )}
              <button
                onClick={handleSaveImage}
                disabled={savingImage || imageInput === (status.sandboxImage || '')}
                className={btnBase}
                style={{ background: 'var(--brand)', color: '#fff', opacity: (savingImage || imageInput === (status.sandboxImage || '')) ? 0.5 : 1 }}
              >
                {savingImage ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
                Save Image
              </button>
            </div>
          </div>
        )}

        {/* Dockerfile-based image build */}
        {canManage && (
          <div className="rounded-xl p-4 space-y-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <div className="flex items-center justify-between">
              <div>
                <h4 className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Dockerfile Recipe</h4>
                <p className="text-[11px] mt-0.5" style={{ color: 'var(--text-muted)' }}>
                  Write Dockerfile instructions to customize the sandbox image. <code>FROM</code> is added automatically.
                </p>
              </div>
              {dockerfileBody !== savedDockerfileBody && (
                <span className="text-[10px] px-2 py-0.5 rounded-full" style={{ background: 'rgba(234, 179, 8, 0.1)', color: '#eab308', border: '1px solid rgba(234, 179, 8, 0.3)' }}>
                  Unsaved
                </span>
              )}
            </div>

            <div className="rounded-lg overflow-hidden" style={{ border: '1px solid var(--border-color)' }}>
              <CodeMirror
                value={dockerfileBody}
                onChange={setDockerfileBody}
                height="200px"
                extensions={[
                  StreamLanguage.define(dockerFile),
                  search({ scrollToMatch: (range) => EditorView.scrollIntoView(range, { y: 'center', yMargin: 100 }) }),
                  highlightSelectionMatches(),
                  keymap.of(searchKeymap),
                ]}
                theme={theme === 'dark' ? 'dark' : 'light'}
                editable={!buildingImage}
                className="text-xs"
                placeholder={"# Example:\nRUN apt-get update && apt-get install -y curl git\nRUN pip install requests\nENV MY_VAR=value"}
                basicSetup={{
                  lineNumbers: true,
                  highlightActiveLineGutter: true,
                  highlightActiveLine: true,
                  foldGutter: false,
                }}
              />
            </div>

            <div className="flex items-center gap-2 p-2 rounded-lg" style={{ background: 'rgba(59, 130, 246, 0.05)', border: '1px solid rgba(59, 130, 246, 0.15)' }}>
              <Info size={12} style={{ color: '#3b82f6', flexShrink: 0 }} />
              <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
                Allowed: RUN, ENV, ARG, WORKDIR, COPY --from=, LABEL. Forbidden: FROM, ENTRYPOINT, CMD, EXPOSE (controlled by platform).
              </span>
            </div>

            {/* Build log */}
            {buildLog.length > 0 && (
              <div className="font-mono text-[11px] space-y-0.5 max-h-48 overflow-y-auto p-3 rounded-lg"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                {buildLog.map((line, i) => (
                  <div key={i}>{line}</div>
                ))}
                {buildingImage && (
                  <div className="flex items-center gap-2 mt-1">
                    <Loader2 size={10} className="animate-spin" /> Building...
                  </div>
                )}
              </div>
            )}

            <div className="flex items-center justify-end gap-2">
              {dockerfileBody !== savedDockerfileBody && (
                <button
                  onClick={async () => {
                    setSavingDockerfile(true)
                    setError('')
                    const result = await saveTeamDockerfile(teamSlug, dockerfileBody)
                    if (result.ok) {
                      setSavedDockerfileBody(dockerfileBody)
                      setSuccess('Dockerfile saved')
                    } else {
                      setError(result.error || 'Failed to save Dockerfile')
                    }
                    setSavingDockerfile(false)
                  }}
                  disabled={savingDockerfile}
                  className={btnBase}
                  style={{ color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                >
                  {savingDockerfile ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
                  Save
                </button>
              )}
              {buildingImage && (
                <button
                  onClick={() => { buildAbortRef.current?.(); setBuildingImage(false); setBuildLog(prev => [...prev, '--- Cancelled ---']) }}
                  className={btnBase}
                  style={{ color: '#ef4444', border: '1px solid rgba(239, 68, 68, 0.3)' }}
                >
                  Cancel
                </button>
              )}
              <button
                onClick={() => {
                  if (!dockerfileBody.trim()) return
                  setError('')
                  setSuccess('')
                  setBuildLog([])
                  setBuildingImage(true)
                  // Save first, then build
                  saveTeamDockerfile(teamSlug, dockerfileBody).then(saveResult => {
                    if (!saveResult.ok) {
                      setError(saveResult.error || 'Failed to save Dockerfile before build')
                      setBuildingImage(false)
                      return
                    }
                    setSavedDockerfileBody(dockerfileBody)
                    const { abort } = buildTeamImage({
                      dockerfileBody,
                      teamSlug,
                      onProgress: (msg) => setBuildLog(prev => [...prev, msg]),
                      onDone: (result) => {
                        setBuildingImage(false)
                        setSuccess(`Build complete! Image: ${result.image}`)
                        setImageInput(result.image)
                        loadStatus()
                      },
                      onError: (err) => {
                        setBuildingImage(false)
                        setError(err)
                      },
                    })
                    buildAbortRef.current = abort
                  })
                }}
                disabled={buildingImage || !dockerfileBody.trim()}
                className={btnBase}
                style={{ background: 'var(--brand)', color: '#fff', opacity: (buildingImage || !dockerfileBody.trim()) ? 0.5 : 1 }}
              >
                {buildingImage ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
                {buildingImage ? 'Building...' : 'Build Image'}
              </button>
            </div>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full gap-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
            <Box size={16} style={{ color: 'var(--brand)' }} />
            Team Container Environment
          </h3>
          <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
            Customize the sandbox environment for this team. Installed packages and tools will be available in all fleet sessions.
          </p>
        </div>

        {/* Status badge */}
        {status && (
          <div className="flex items-center gap-2">
            {status.exists ? (
              <span className="flex items-center gap-1 px-2 py-1 text-xs rounded-full" style={{
                background: status.running ? 'rgba(34, 197, 94, 0.1)' : 'rgba(234, 179, 8, 0.1)',
                color: status.running ? '#22c55e' : '#eab308',
                border: `1px solid ${status.running ? 'rgba(34, 197, 94, 0.3)' : 'rgba(234, 179, 8, 0.3)'}`,
              }}>
                <span className="w-1.5 h-1.5 rounded-full" style={{ background: status.running ? '#22c55e' : '#eab308' }} />
                {status.running ? 'Running' : 'Stopped'}
              </span>
            ) : (
              <span className="flex items-center gap-1 px-2 py-1 text-xs rounded-full" style={{
                background: 'rgba(107, 114, 128, 0.1)',
                color: 'var(--text-muted)',
                border: '1px solid rgba(107, 114, 128, 0.2)',
              }}>
                Not Created
              </span>
            )}
            {status.saved && (
              <span className="flex items-center gap-1 px-2 py-1 text-xs rounded-full" style={{
                background: 'color-mix(in oklab, var(--brand) 10%, transparent)',
                color: 'var(--brand)',
                border: '1px solid color-mix(in oklab, var(--brand) 30%, transparent)',
              }}>
                <CheckCircle2 size={10} />
                Active
              </span>
            )}
          </div>
        )}
      </div>

      {/* Messages */}
      {error && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-xs" style={{
          background: 'rgba(239, 68, 68, 0.1)',
          color: 'var(--danger)',
          border: '1px solid rgba(239, 68, 68, 0.2)',
        }}>
          <AlertCircle size={14} />
          <span>{error}</span>
        </div>
      )}
      {success && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-xs" style={{
          background: 'rgba(34, 197, 94, 0.1)',
          color: '#22c55e',
          border: '1px solid rgba(34, 197, 94, 0.2)',
        }}>
          <CheckCircle2 size={14} />
          <span>{success}</span>
        </div>
      )}

      {/* Action buttons */}
      {canManage && (
        <div className="flex items-center gap-2 flex-wrap">
          {!status?.exists && (
            <button
              onClick={handleCreate}
              disabled={!!actionLoading}
              className={btnBase}
              style={{ background: 'var(--brand)', color: '#fff' }}
            >
              {actionLoading === 'create' ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
              Create & Start
            </button>
          )}

          {status?.exists && !status.running && (
            <button
              onClick={handleStart}
              disabled={!!actionLoading}
              className={btnBase}
              style={{ background: 'var(--brand)', color: '#fff' }}
            >
              {actionLoading === 'start' ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
              Start
            </button>
          )}

          {status?.exists && status.running && (
            <button
              onClick={handleSave}
              disabled={!!actionLoading}
              className={btnBase}
              style={{ background: 'rgba(34, 197, 94, 0.15)', color: '#22c55e', border: '1px solid rgba(34, 197, 94, 0.3)' }}
            >
              {actionLoading === 'save' ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
              Save as Team Default
            </button>
          )}

          {status?.exists && (
            <>
              <button
                onClick={handleRestore}
                disabled={!!actionLoading}
                className={btnBase}
                style={{ background: 'rgba(234, 179, 8, 0.1)', color: '#eab308', border: '1px solid rgba(234, 179, 8, 0.3)' }}
              >
                {actionLoading === 'restore' ? <Loader2 size={12} className="animate-spin" /> : <RotateCcw size={12} />}
                Restore from Base
              </button>
              <button
                onClick={handleDelete}
                disabled={!!actionLoading}
                className={btnBase}
                style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)', border: '1px solid rgba(239, 68, 68, 0.2)' }}
              >
                {actionLoading === 'delete' ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                Delete
              </button>
            </>
          )}
        </div>
      )}

      {/* Starting indicator — shown while pod is being provisioned */}
      {showTerminal && status?.exists && !status.running && (
        <div className="flex-1 min-h-0 rounded-lg overflow-hidden flex items-center justify-center" style={{ border: '1px solid var(--border-color)', background: theme === 'dark' ? '#1a1a2e' : '#ffffff' }}>
          <div className="text-center">
            <Loader2 size={24} className="animate-spin mx-auto mb-2" style={{ color: 'var(--brand)' }} />
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Starting container...</p>
          </div>
        </div>
      )}

      {/* Terminal */}
      {showTerminal && status?.exists && status.running && (
        <div className="flex-1 min-h-0 rounded-lg overflow-hidden" style={{ border: '1px solid var(--border-color)' }}>
          <Suspense fallback={
            <div className="flex items-center justify-center h-full py-12">
              <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
            </div>
          }>
            <TeamContainerTerminal
              teamSlug={teamSlug}
              theme={theme}
              onDisconnect={handleTerminalDisconnect}
            />
          </Suspense>
        </div>
      )}

      {/* Help text when no container exists */}
      {!status?.exists && (
        <div className="flex-1 flex items-center justify-center" style={{ color: 'var(--text-muted)' }}>
          <div className="text-center max-w-sm">
            <Box size={48} className="mx-auto mb-4 opacity-30" />
            <p className="text-sm font-medium mb-2">No team container configured</p>
            <p className="text-xs">
              Create a team container to pre-install packages, configure tools, and set up the environment
              that all fleet sessions for this team will inherit.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}
