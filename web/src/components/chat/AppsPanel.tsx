import { useState, useEffect, useCallback } from 'react'
import { X, AppWindow, ChevronLeft, ChevronRight, Code, Copy, Check, Zap, Eye } from 'lucide-react'
import AppPreview from './AppPreview'
import type { AppPreviewMessage, ChatMsg } from './chatTypes'

interface AppInfo {
  appId: string
  title: string
  latestCode: string
  latestVersion: number
  versions: AppPreviewMessage[]
  isActive: boolean
}

interface AppsPanelProps {
  messages: ChatMsg[]
  activeAppId: string | null
  onClose: () => void
}

export default function AppsPanel({ messages, activeAppId, onClose }: AppsPanelProps) {
  const [overlayApp, setOverlayApp] = useState<AppInfo | null>(null)
  const [overlayVersionIdx, setOverlayVersionIdx] = useState(0)
  const [copied, setCopied] = useState(false)
  const [showCode, setShowCode] = useState(false)

  // Collect and group app_preview messages
  const apps: AppInfo[] = (() => {
    const appMap = new Map<string, AppInfo>()
    const appPreviews = messages.filter(
      (m): m is AppPreviewMessage => m.type === 'app_preview'
    )
    for (const msg of appPreviews) {
      const key = msg.appId || msg.title
      const existing = appMap.get(key)
      if (existing) {
        existing.versions.push(msg)
        existing.latestCode = msg.code
        existing.latestVersion = msg.version
        existing.title = msg.title
      } else {
        appMap.set(key, {
          appId: msg.appId || msg.title,
          title: msg.title,
          latestCode: msg.code,
          latestVersion: msg.version,
          versions: [msg],
          isActive: false,
        })
      }
    }
    // Mark active app
    for (const app of appMap.values()) {
      app.isActive = activeAppId != null && app.appId === activeAppId
    }
    return Array.from(appMap.values())
  })()

  // Close overlay on Escape
  useEffect(() => {
    if (!overlayApp) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOverlayApp(null)
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [overlayApp])

  // Prevent body scroll when overlay is open
  useEffect(() => {
    if (overlayApp) {
      document.body.style.overflow = 'hidden'
    } else {
      document.body.style.overflow = ''
    }
    return () => { document.body.style.overflow = '' }
  }, [overlayApp])

  const openApp = useCallback((app: AppInfo) => {
    setOverlayApp(app)
    setOverlayVersionIdx(app.versions.length - 1) // Show latest
    setShowCode(false)
    setCopied(false)
  }, [])

  const closeOverlay = useCallback(() => {
    setOverlayApp(null)
    setShowCode(false)
    setCopied(false)
  }, [])

  const handleCopy = useCallback(() => {
    if (!overlayApp) return
    const code = overlayApp.versions[overlayVersionIdx]?.code || overlayApp.latestCode
    navigator.clipboard.writeText(code)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [overlayApp, overlayVersionIdx])

  const displayedVersion = overlayApp?.versions[overlayVersionIdx]
  const canGoPrev = overlayApp != null && overlayVersionIdx > 0
  const canGoNext = overlayApp != null && overlayVersionIdx < (overlayApp?.versions.length ?? 0) - 1

  return (
    <>
      {/* ===== Right-side Apps List Panel ===== */}
      <div
        className="flex flex-col h-full"
        style={{
          width: '320px',
          minWidth: '280px',
          borderLeft: '1px solid var(--border-color)',
          background: 'var(--bg-primary)',
        }}
      >
        {/* Panel header */}
        <div
          className="flex items-center gap-2 px-4 py-2.5 shrink-0"
          style={{ borderBottom: '1px solid var(--border-color)' }}
        >
          <AppWindow size={16} style={{ color: 'var(--text-muted)' }} />
          <span className="text-sm font-medium flex-1" style={{ color: 'var(--text-primary)' }}>
            Apps ({apps.length})
          </span>
          <button
            onClick={onClose}
            className="p-1 rounded hover:opacity-70"
            style={{ color: 'var(--text-muted)' }}
          >
            <X size={16} />
          </button>
        </div>

        {/* Apps list */}
        <div className="flex-1 overflow-y-auto">
          {apps.map(app => (
            <button
              key={app.appId}
              onClick={() => openApp(app)}
              className="w-full text-left flex items-center gap-3 px-4 py-3 transition-colors hover:bg-white/5"
              style={{
                borderBottom: '1px solid var(--border-color)',
                background: overlayApp?.appId === app.appId
                  ? 'var(--accent-bg, rgba(59, 130, 246, 0.08))'
                  : 'transparent',
              }}
            >
              {/* App icon */}
              <div
                className="flex items-center justify-center w-8 h-8 rounded shrink-0"
                style={{
                  background: app.isActive
                    ? 'var(--brand-muted)'
                    : 'rgba(59, 130, 246, 0.1)',
                }}
              >
                <AppWindow size={15} style={{
                  color: app.isActive ? 'var(--brand)' : '#60a5fa',
                }} />
              </div>

              {/* App info */}
              <div className="flex flex-col min-w-0 flex-1 gap-0.5">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                    {app.title}
                  </span>
                  <span
                    className="text-[10px] px-1.5 py-0.5 rounded-full shrink-0 font-medium"
                    style={{
                      background: 'rgba(59, 130, 246, 0.15)',
                      color: '#60a5fa',
                    }}
                  >
                    v{app.latestVersion}
                  </span>
                </div>
                {app.isActive && (
                  <span className="flex items-center gap-1 text-[10px]" style={{ color: 'var(--brand)' }}>
                    <Zap size={9} />
                    Refining
                  </span>
                )}
                {!app.isActive && app.versions.length > 1 && (
                  <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
                    {app.versions.length} versions
                  </span>
                )}
              </div>

              {/* View indicator */}
              <Eye size={13} className="shrink-0" style={{ color: 'var(--text-muted)', opacity: 0.5 }} />
            </button>
          ))}

          {apps.length === 0 && (
            <div className="text-xs text-center py-8" style={{ color: 'var(--text-muted)' }}>
              No apps generated yet
            </div>
          )}
        </div>
      </div>

      {/* ===== Full-screen Overlay Modal ===== */}
      {overlayApp && displayedVersion && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center"
          style={{ background: 'rgba(0, 0, 0, 0.6)', backdropFilter: 'blur(4px)' }}
          onClick={(e) => { if (e.target === e.currentTarget) closeOverlay() }}
        >
          <div
            className="flex flex-col rounded-xl shadow-2xl overflow-hidden"
            style={{
              width: '80vw',
              maxWidth: '1200px',
              height: '90vh',
              maxHeight: '90vh',
              background: 'var(--bg-primary)',
              border: '1px solid var(--border-color)',
            }}
          >
            {/* Overlay header */}
            <div
              className="flex items-center gap-3 px-5 py-3 shrink-0"
              style={{ borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
            >
              {/* App icon + title */}
              <div
                className="flex items-center justify-center w-8 h-8 rounded"
                style={{ background: 'rgba(59, 130, 246, 0.12)' }}
              >
                <AppWindow size={16} style={{ color: '#60a5fa' }} />
              </div>
              <div className="flex flex-col min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                    {displayedVersion.title || overlayApp.title}
                  </span>
                  {overlayApp.isActive && (
                    <span
                      className="flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium"
                      style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}
                    >
                      <Zap size={9} />
                      Refining
                    </span>
                  )}
                </div>
              </div>

              {/* Version navigation */}
              {overlayApp.versions.length > 1 && (
                <div className="flex items-center gap-1">
                  <button
                    onClick={() => canGoPrev && setOverlayVersionIdx(overlayVersionIdx - 1)}
                    disabled={!canGoPrev}
                    className="p-1 rounded transition-colors disabled:opacity-30 disabled:cursor-default"
                    style={{ color: 'var(--text-muted)' }}
                    title="Previous version"
                  >
                    <ChevronLeft size={16} />
                  </button>
                  <span className="text-xs tabular-nums px-1" style={{ color: 'var(--text-muted)' }}>
                    v{displayedVersion.version}
                  </span>
                  <button
                    onClick={() => canGoNext && setOverlayVersionIdx(overlayVersionIdx + 1)}
                    disabled={!canGoNext}
                    className="p-1 rounded transition-colors disabled:opacity-30 disabled:cursor-default"
                    style={{ color: 'var(--text-muted)' }}
                    title="Next version"
                  >
                    <ChevronRight size={16} />
                  </button>
                </div>
              )}

              {/* Code toggle */}
              <button
                onClick={() => setShowCode(!showCode)}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded text-xs font-medium"
                style={{
                  background: showCode ? 'var(--accent-bg, rgba(59, 130, 246, 0.15))' : 'var(--bg-primary)',
                  color: showCode ? 'var(--accent-color, #60a5fa)' : 'var(--text-secondary)',
                  border: '1px solid var(--border-color)',
                }}
              >
                <Code size={12} />
                <span>Code</span>
              </button>

              {/* Copy button */}
              <button
                onClick={handleCopy}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded text-xs font-medium"
                style={{
                  background: 'var(--bg-primary)',
                  color: copied ? '#4ade80' : 'var(--text-secondary)',
                  border: '1px solid var(--border-color)',
                }}
              >
                {copied ? <Check size={12} /> : <Copy size={12} />}
                <span>{copied ? 'Copied' : 'Copy'}</span>
              </button>

              {/* Close button */}
              <button
                onClick={closeOverlay}
                className="p-1.5 rounded hover:opacity-70 transition-opacity"
                style={{ color: 'var(--text-muted)' }}
              >
                <X size={18} />
              </button>
            </div>

            {/* Overlay content */}
            <div className="flex-1 overflow-hidden flex flex-col">
              {/* Code panel (collapsible) */}
              {showCode && (
                <div
                  className="shrink-0"
                  style={{ borderBottom: '1px solid var(--border-color)', maxHeight: '35vh', overflow: 'auto' }}
                >
                  <pre
                    className="px-6 py-4 text-[12px] font-mono leading-relaxed"
                    style={{ color: 'var(--text-secondary)' }}
                  >
                    <code>{displayedVersion.code}</code>
                  </pre>
                </div>
              )}

              {/* Live preview */}
              <div className="flex-1 overflow-auto p-4">
                <AppPreview
                  code={displayedVersion.code}
                  maxHeight={9999}
                />
              </div>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
