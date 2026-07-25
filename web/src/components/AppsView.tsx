import { useState, useEffect, useCallback } from 'react'
import { AppWindow, Trash2, Code, ArrowLeft, Clock, Sparkles, Upload, GitFork } from 'lucide-react'
import { fetchApps, fetchApp, deleteApp, saveApp } from '../api/apps'
import type { AppListItem, VisualApp, AppModelStatus } from '../api/apps'
import { fetchAvailableProviders } from '../api/studioChat'
import AppPreview from './chat/AppPreview'
import CodeDrawer from './CodeDrawer'
import AppModelPicker from './AppModelPicker'

interface AppsViewProps {
  theme: string
  appName?: string
  isPlatformMode?: boolean
  onNavigate?: (path: string) => void
  onImproveApp?: (message: string, systemContext: string) => void
  onPublishApp?: (app: AppListItem) => void
  onForkApp?: (app: AppListItem) => void
}

function EmptyState() {
  return (
    <div className="flex flex-1 items-center justify-center">
      <div className="text-center">
        <AppWindow size={48} className="mx-auto mb-4 text-[color:var(--success)]/30" />
        <h2 className="mb-2 text-lg font-semibold text-foreground">Visual Apps</h2>
        <p className="max-w-md text-sm text-muted-foreground">
          No apps saved yet. Generate a visual app in Chat by asking to build a
          dashboard, calculator, form, or any interactive UI — then click Save to
          keep it here.
        </p>
      </div>
    </div>
  )
}

function formatDate(dateStr: string) {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - d.getTime()
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 7) return `${diffDay}d ago`
  return d.toLocaleDateString()
}

export default function AppsView({ theme, appName, isPlatformMode, onNavigate, onImproveApp, onPublishApp, onForkApp }: AppsViewProps) {
  const [apps, setApps] = useState<AppListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedApp, setSelectedApp] = useState<VisualApp | null>(null)
  const [showCode, setShowCode] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<{ slug: string; name: string; scope?: string } | null>(null)
  const [codeContent, setCodeContent] = useState('')
  const [isSaving, setIsSaving] = useState(false)
  const [saveStatus, setSaveStatus] = useState<'saved' | 'error' | null>(null)
  const [availableProviders, setAvailableProviders] = useState<string[]>([])

  const loadApps = useCallback(async () => {
    try {
      const data = await fetchApps()
      setApps(data.apps || [])
    } catch {
      // Ignore errors
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadApps()
    fetchAvailableProviders().then(setAvailableProviders).catch(() => setAvailableProviders([]))
  }, [loadApps])

  // Listen for apps-updated events (from chat save flow)
  useEffect(() => {
    const handler = () => loadApps()
    window.addEventListener('astonish:apps-updated', handler)
    return () => window.removeEventListener('astonish:apps-updated', handler)
  }, [loadApps])

  // Load app from URL param (deep link or refresh)
  useEffect(() => {
    if (appName && !selectedApp) {
      fetchApp(appName)
        .then(app => {
          setSelectedApp(app)
          setCodeContent(app.code)
          setShowCode(false)
          setSaveStatus(null)
        })
        .catch(() => {
          // App not found — navigate back to list
          if (onNavigate) onNavigate('/apps')
        })
    } else if (!appName && selectedApp) {
      // URL navigated back to /apps — clear selection
      setSelectedApp(null)
      setShowCode(false)
    }
  }, [appName]) // eslint-disable-line react-hooks/exhaustive-deps

  const handleOpenApp = useCallback(async (slug: string) => {
    try {
      const app = await fetchApp(slug)
      setSelectedApp(app)
      setCodeContent(app.code)
      setShowCode(false)
      setSaveStatus(null)
      if (onNavigate) {
        onNavigate(`/apps/${encodeURIComponent(slug)}`)
      }
    } catch {
      // Ignore
    }
  }, [onNavigate])

  const handleDeleteApp = useCallback(async (slug: string, scope?: string) => {
    try {
      await deleteApp(slug, scope)
      // When deleting with a specific scope, only remove matching items
      setApps(prev => prev.filter(a => !(a.slug === slug && (!scope || a.scope === scope))))
      if (appName === slug) {
        setSelectedApp(null)
        if (onNavigate) onNavigate('/apps')
      }
    } catch {
      // Ignore
    }
    setDeleteConfirm(null)
  }, [appName, onNavigate])

  const handleSaveCode = useCallback(async () => {
    if (!selectedApp) return
    setIsSaving(true)
    setSaveStatus(null)
    try {
      await saveApp(selectedApp.name, {
        description: selectedApp.description,
        code: codeContent,
        version: selectedApp.version,
        sessionId: selectedApp.sessionId,
      })
      setSelectedApp(prev => prev ? { ...prev, code: codeContent } : prev)
      setSaveStatus('saved')
      setTimeout(() => setSaveStatus(null), 3000)
    } catch {
      setSaveStatus('error')
    } finally {
      setIsSaving(false)
    }
  }, [selectedApp, codeContent])

  const handleImproveWithAI = useCallback(() => {
    if (!selectedApp || !onImproveApp) return
    const systemContext = [
      '## Active App Refinement\n',
      `The user wants to improve a saved visual app called "${selectedApp.name}" (version ${selectedApp.version}).`,
      '',
      'IMPORTANT: The app preview is ALREADY displayed to the user. Do NOT generate or output any code right now.',
      'Do NOT output an ```astonish-app code fence in this response.',
      'Simply acknowledge that you can see the app and ask the user what changes they would like to make.',
      'Only output code inside an ```astonish-app fence AFTER the user describes specific changes they want.',
      '',
      'When the user later describes changes, apply them to the source code below and output the COMPLETE updated component.',
      'You MUST output the full component inside an ```astonish-app code fence — do NOT output a diff or partial snippet.',
      'Preserve all existing functionality unless the user explicitly asks to remove it.',
      '',
      '### Current Source Code\n',
      '```jsx',
      selectedApp.code,
      '```',
    ].join('\n')
    onImproveApp('I want to improve this app.', systemContext)
  }, [selectedApp, onImproveApp])

  // Split apps by scope for platform mode display
  const personalApps = isPlatformMode ? apps.filter(a => a.scope === 'personal') : apps
  const teamApps = isPlatformMode ? apps.filter(a => a.scope === 'team') : []
  const hasApps = personalApps.length > 0 || teamApps.length > 0

  // Runner view
  if (selectedApp) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden" style={{ background: 'var(--bg-primary)' }}>
        {/* Toolbar */}
        <div
          className="flex items-center gap-3 px-4 py-2 shrink-0"
          style={{ borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
        >
          <button
            onClick={() => {
              setSelectedApp(null)
              if (onNavigate) onNavigate('/apps')
            }}
            className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors cursor-pointer"
            style={{ color: 'var(--text-secondary)' }}
          >
            <ArrowLeft size={14} />
            Back
          </button>

          <div className="flex-1 flex items-center gap-2 min-w-0">
            <AppWindow size={16} style={{ color: '#10b981', flexShrink: 0 }} />
            <span className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>
              {selectedApp.name}
            </span>
            {selectedApp.version > 1 && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full shrink-0"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                v{selectedApp.version}
              </span>
            )}
            <AppModelPicker
              slug={appName || selectedApp.name}
              availableProviders={availableProviders}
              initialStatus={{
                pinnedProvider: selectedApp.pinnedProvider || null,
                pinnedModel: selectedApp.pinnedModel || null,
                effectiveProvider: selectedApp.effectiveProvider || '',
                effectiveModel: selectedApp.effectiveModel || '',
              }}
              onUpdate={(status: AppModelStatus) => {
                setSelectedApp((prev) => prev ? {
                  ...prev,
                  pinnedProvider: status.pinnedProvider || '',
                  pinnedModel: status.pinnedModel || '',
                  effectiveProvider: status.effectiveProvider,
                  effectiveModel: status.effectiveModel,
                } : prev)
              }}
            />
          </div>

          {onImproveApp && (
            <button
              onClick={handleImproveWithAI}
              className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors cursor-pointer"
              style={{ color: 'var(--text-muted)' }}
              title="Improve this app with AI in Chat"
            >
              <Sparkles size={14} />
              Improve with AI
            </button>
          )}

          <button
            onClick={() => setShowCode(!showCode)}
            className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors cursor-pointer"
            style={{
              background: showCode ? 'rgba(6, 182, 212, 0.15)' : 'transparent',
              color: showCode ? '#22d3ee' : 'var(--text-muted)',
            }}
            title={showCode ? 'Hide code' : 'View code'}
          >
            <Code size={14} />
            {showCode ? 'Hide Code' : 'View Code'}
          </button>

          <button
            onClick={() => setDeleteConfirm({ slug: appName || selectedApp.name, name: selectedApp.name })}
            className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors cursor-pointer"
            style={{ color: 'var(--text-muted)' }}
            title="Delete this app"
          >
            <Trash2 size={14} />
          </button>
        </div>

        {/* Delete confirmation */}
        {deleteConfirm && (
          <div className="flex items-center gap-2 px-4 py-2 text-xs"
            style={{ background: 'rgba(239, 68, 68, 0.1)', borderBottom: '1px solid rgba(239, 68, 68, 0.2)' }}>
            <span style={{ color: '#ef4444' }}>Delete this app permanently?</span>
            <button
              onClick={() => handleDeleteApp(deleteConfirm.slug, deleteConfirm.scope)}
              className="px-2 py-0.5 rounded text-white cursor-pointer"
              style={{ background: '#ef4444' }}
            >
              Delete
            </button>
            <button
              onClick={() => setDeleteConfirm(null)}
              className="px-2 py-0.5 rounded cursor-pointer"
              style={{ color: 'var(--text-muted)', background: 'var(--bg-tertiary)' }}
            >
              Cancel
            </button>
          </div>
        )}

        {/* Content area */}
        <div className="flex-1 flex flex-col overflow-hidden">
          {/* Preview iframe */}
          <div className={`${showCode ? 'h-1/2' : 'flex-1'} overflow-auto p-4`}>
            <AppPreview code={selectedApp.code} maxHeight={9999} appName={selectedApp.name} />
          </div>

          {/* Bottom code drawer */}
          {showCode && (
            <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
              <CodeDrawer
                code={codeContent}
                onChange={(val) => setCodeContent(val)}
                onClose={() => setShowCode(false)}
                theme={theme === 'dark' ? 'dark' : 'light'}
                title={selectedApp.name}
                subtitle="JSX / React component"
                onSave={handleSaveCode}
                isSaving={isSaving}
                saveStatus={saveStatus}
              />
            </div>
          )}
        </div>
      </div>
    )
  }

  // List view
  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <span className="text-sm" style={{ color: 'var(--text-muted)' }}>Loading apps...</span>
      </div>
    )
  }

  if (!hasApps) {
    return <EmptyState />
  }

  const renderAppCard = (app: AppListItem) => (
    <div
      key={`${app.scope || 'local'}-${app.slug}`}
      className="group rounded-xl overflow-hidden flex flex-col transition-all hover:shadow-lg"
      style={{
        border: '1px solid var(--border-color)',
        background: 'var(--bg-secondary)',
      }}
    >
      <div className="p-4 flex-1 cursor-pointer" onClick={() => handleOpenApp(app.slug)}>
        <div className="flex items-start justify-between mb-2">
          <div className="flex items-center gap-2 min-w-0">
            <AppWindow size={16} style={{ color: '#10b981', flexShrink: 0 }} />
            <span className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>
              {app.name}
            </span>
          </div>
          <div className="flex items-center gap-1.5 shrink-0 ml-2">
            {isPlatformMode && app.scope && (
              <span
                className="text-[10px] px-1.5 py-0.5 rounded-full"
                style={{
                  background: app.scope === 'personal' ? 'rgba(99, 102, 241, 0.15)' : 'rgba(16, 185, 129, 0.15)',
                  color: app.scope === 'personal' ? '#818cf8' : '#34d399',
                }}
              >
                {app.scope === 'personal' ? 'Personal' : 'Team'}
              </span>
            )}
            {app.version > 1 && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                v{app.version}
              </span>
            )}
          </div>
        </div>

        {app.description && app.description !== app.name && (
          <p className="text-xs mb-3 line-clamp-2" style={{ color: 'var(--text-muted)' }}>
            {app.description}
          </p>
        )}
      </div>

      <div className="flex items-center justify-between mt-auto px-4 pb-3 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
        <div className="flex items-center gap-1 text-[10px]" style={{ color: 'var(--text-muted)' }}>
          <Clock size={10} />
          <span>{formatDate(app.updatedAt)}</span>
        </div>
        <div className="flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
          <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
            {isPlatformMode && app.scope === 'personal' && onPublishApp && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onPublishApp(app)
                }}
                className="p-1 rounded hover:bg-blue-500/20 transition-all"
                title="Publish to Team"
              >
                <Upload size={12} className="text-blue-400" />
              </button>
            )}
            {isPlatformMode && app.scope === 'team' && onForkApp && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  onForkApp(app)
                }}
                className="p-1 rounded hover:bg-green-500/20 transition-all"
                title="Fork to Personal"
              >
                <GitFork size={12} className="text-green-400" />
              </button>
            )}
            <button
              onClick={(e) => {
                e.stopPropagation()
                setDeleteConfirm({ slug: app.slug, name: app.name, scope: app.scope })
              }}
              className="p-1 rounded transition-colors cursor-pointer"
              style={{ color: 'var(--text-muted)' }}
              title="Delete app"
            >
              <Trash2 size={12} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )

  return (
    <div className="flex-1 overflow-auto p-6" style={{ background: 'var(--bg-primary)' }}>
      <div className="max-w-5xl mx-auto">
        <div className="flex items-center gap-3 mb-6">
          <AppWindow size={20} style={{ color: '#10b981' }} />
          <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Visual Apps</h1>
          <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
            {apps.length}
          </span>
        </div>

        {/* Platform mode: show personal then team sections */}
        {isPlatformMode && (personalApps.length > 0 || teamApps.length > 0) ? (
          <>
            {personalApps.length > 0 && (
              <>
                <div className="flex items-center gap-2 mb-3">
                  <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Personal</span>
                  <span className="text-[10px] px-1.5 py-0.5 rounded-full"
                    style={{ background: 'rgba(99, 102, 241, 0.15)', color: '#818cf8' }}>
                    {personalApps.length}
                  </span>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 mb-6">
                  {personalApps.map(renderAppCard)}
                </div>
              </>
            )}
            {teamApps.length > 0 && (
              <>
                <div className="flex items-center gap-2 mb-3">
                  <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Team</span>
                  <span className="text-[10px] px-1.5 py-0.5 rounded-full"
                    style={{ background: 'rgba(16, 185, 129, 0.15)', color: '#34d399' }}>
                    {teamApps.length}
                  </span>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                  {teamApps.map(renderAppCard)}
                </div>
              </>
            )}
          </>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {apps.map(renderAppCard)}
          </div>
        )}

        {/* Inline delete confirmation for list view */}
        {deleteConfirm && !selectedApp && (
          <div className="fixed inset-0 flex items-center justify-center z-50" style={{ background: 'rgba(0,0,0,0.5)' }}>
            <div className="rounded-xl p-6 max-w-sm w-full mx-4"
              style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
              <h3 className="text-sm font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Delete App</h3>
              <p className="text-xs mb-4" style={{ color: 'var(--text-muted)' }}>
                Are you sure you want to delete <strong>{deleteConfirm.name}</strong>? This cannot be undone.
              </p>
              <div className="flex gap-2 justify-end">
                <button
                  onClick={() => setDeleteConfirm(null)}
                  className="px-3 py-1.5 rounded text-xs cursor-pointer"
                  style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
                >
                  Cancel
                </button>
                <button
              onClick={() => handleDeleteApp(deleteConfirm.slug, deleteConfirm.scope)}
                  className="px-3 py-1.5 rounded text-xs text-white cursor-pointer"
                  style={{ background: '#ef4444' }}
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
