import { useState, useEffect, useRef, useCallback } from 'react'
import { X, FileText, Download, ChevronDown, Loader, FilePlus, Edit3, Eye, Film } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './markdownComponents'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import { fetchArtifactContent, getArtifactDownloadUrl, getArtifactPDFUrl } from '../../api/studioChat'
import { artifactMediaKind, downloadLabelForArtifact } from '../../utils/artifactMedia'
import ArtifactMediaPlayer from './ArtifactMediaPlayer'
import type { SessionArtifact } from './chatTypes'

interface FilePanelProps {
  artifacts: SessionArtifact[]
  initialPath?: string | null
  sessionId?: string | null
  onClose: () => void
}

// Icon for the artifact based on type / tool name
function ArtifactIcon({ toolName, fileType, size = 14 }: { toolName: string; fileType?: string; size?: number }) {
  if (fileType === 'Video') {
    return <Film size={size} className="text-rose-400" />
  }
  if (toolName === 'edit_file') return <Edit3 size={size} className="text-green-400" />
  return <FilePlus size={size} className="text-green-400" />
}

// Badge color for file type
function fileTypeBadgeStyle(fileType: string) {
  const colors: Record<string, { bg: string; text: string }> = {
    Markdown: { bg: 'rgba(59, 130, 246, 0.15)', text: '#60a5fa' },
    Python: { bg: 'rgba(234, 179, 8, 0.15)', text: '#facc15' },
    JSON: { bg: 'var(--brand-muted)', text: 'var(--brand)' },
    Go: { bg: 'rgba(6, 182, 212, 0.15)', text: '#22d3ee' },
    HTML: { bg: 'rgba(249, 115, 22, 0.15)', text: '#fb923c' },
    CSS: { bg: 'rgba(59, 130, 246, 0.15)', text: '#60a5fa' },
    Shell: { bg: 'rgba(34, 197, 94, 0.15)', text: '#4ade80' },
    Video: { bg: 'rgba(244, 63, 94, 0.15)', text: '#fb7185' },
  }
  const c = colors[fileType] || { bg: 'rgba(148, 163, 184, 0.12)', text: 'var(--text-muted)' }
  return { background: c.bg, color: c.text }
}

export default function FilePanel({ artifacts, initialPath, sessionId, onClose }: FilePanelProps) {
  // Overlay state: which file is open in the full-screen viewer
  const [overlayPath, setOverlayPath] = useState<string | null>(null)
  const [content, setContent] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [downloadOpen, setDownloadOpen] = useState(false)
  const [exporting, setExporting] = useState<string | null>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const overlayArtifact = artifacts.find(a => a.path === overlayPath)
  const isMarkdown = overlayArtifact?.fileType === 'Markdown'
  const overlayMediaKind = overlayArtifact
    ? artifactMediaKind(overlayArtifact.fileType, overlayArtifact.fileName)
    : null
  const overlayDownloadLabel = overlayArtifact
    ? downloadLabelForArtifact(overlayArtifact.fileType, overlayArtifact.fileName)
    : 'Download File'

  // When initialPath changes (e.g., clicking "Open in Files" on an inline artifact card),
  // open the overlay directly for that file
  useEffect(() => {
    if (initialPath) setOverlayPath(initialPath)
  }, [initialPath])

  // Load text content when overlay file changes (media uses ArtifactMediaPlayer).
  useEffect(() => {
    if (!overlayPath || !overlayArtifact) {
      setContent('')
      setError(null)
      setLoading(false)
      return
    }
    if (artifactMediaKind(overlayArtifact.fileType, overlayArtifact.fileName)) {
      setContent('')
      setError(null)
      setLoading(false)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    fetchArtifactContent(overlayPath, sessionId || undefined)
      .then(text => { if (!cancelled) setContent(text) })
      .catch(err => { if (!cancelled) setError(err.message) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [overlayPath, overlayArtifact, sessionId])

  // Close download dropdown on outside click
  useEffect(() => {
    if (!downloadOpen) return
    const handler = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setDownloadOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [downloadOpen])

  // Close overlay on Escape key
  useEffect(() => {
    if (!overlayPath) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOverlayPath(null)
        setDownloadOpen(false)
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [overlayPath])

  const closeOverlay = useCallback(() => {
    setOverlayPath(null)
    setDownloadOpen(false)
  }, [])

  const handleDownloadFile = useCallback(() => {
    if (!overlayPath) return
    const url = getArtifactDownloadUrl(overlayPath, sessionId || undefined)
    const a = document.createElement('a')
    a.href = url
    a.download = overlayArtifact?.fileName || 'file'
    a.click()
    setDownloadOpen(false)
  }, [overlayPath, overlayArtifact, sessionId])

  const handleExportPDF = useCallback(async () => {
    if (!overlayPath) return
    setDownloadOpen(false)
    setExporting('pdf')
    try {
      const url = getArtifactPDFUrl(overlayPath, sessionId || undefined)
      const res = await fetch(url)
      if (!res.ok) throw new Error(`PDF generation failed: ${res.statusText}`)
      const blob = await res.blob()
      const { saveAs } = await import('file-saver')
      saveAs(blob, (overlayArtifact?.fileName || 'file').replace(/\.[^.]+$/, '.pdf'))
    } catch (err) {
      console.error('PDF export failed:', err)
    } finally {
      setExporting(null)
    }
  }, [overlayPath, overlayArtifact, sessionId])

  const handleExportDOCX = useCallback(async () => {
    if (!content) return
    setExporting('docx')
    setDownloadOpen(false)
    try {
      const { remarkDocx } = await import('@m2d/remark-docx')
      const { saveAs } = await import('file-saver')

      const processor = unified()
        .use(remarkParse)
        .use(remarkGfm)
        .use(remarkDocx)

      const result = await processor.process(content)
      const blob = await result.result as Blob
      saveAs(blob, (overlayArtifact?.fileName || 'file').replace(/\.[^.]+$/, '.docx'))
    } catch (err) {
      console.error('DOCX export failed:', err)
    } finally {
      setExporting(null)
    }
  }, [content, overlayArtifact])

  return (
    <>
      {/* ===== Right-side File List Panel ===== */}
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
          <FileText size={16} style={{ color: 'var(--text-muted)' }} />
          <span className="text-sm font-medium flex-1" style={{ color: 'var(--text-primary)' }}>
            Files ({artifacts.length})
          </span>
          <button
            onClick={onClose}
            className="p-1 rounded hover:opacity-70"
            style={{ color: 'var(--text-muted)' }}
          >
            <X size={16} />
          </button>
        </div>

        {/* File list */}
        <div className="flex-1 overflow-y-auto">
          {artifacts.map(a => (
            <button
              key={a.path}
              onClick={() => setOverlayPath(a.path)}
              className="w-full text-left flex items-center gap-3 px-4 py-3 transition-colors hover:bg-white/5"
              style={{
                borderBottom: '1px solid var(--border-color)',
                background: a.path === overlayPath ? 'var(--accent-bg, rgba(59, 130, 246, 0.08))' : 'transparent',
              }}
            >
              {/* File icon */}
              <div
                className="flex items-center justify-center w-8 h-8 rounded shrink-0"
                style={{ background: a.fileType === 'Video' ? 'rgba(244, 63, 94, 0.1)' : 'rgba(34, 197, 94, 0.1)' }}
              >
                <ArtifactIcon toolName={a.toolName} fileType={a.fileType} size={15} />
              </div>

              {/* File info */}
              <div className="flex flex-col min-w-0 flex-1 gap-0.5">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                    {a.fileName}
                  </span>
                  <span
                    className="text-[10px] px-1.5 py-0.5 rounded-full shrink-0 font-medium"
                    style={fileTypeBadgeStyle(a.fileType)}
                  >
                    {a.fileType}
                  </span>
                </div>
                <span className="text-[10px] truncate" style={{ color: 'var(--text-muted)' }} title={a.path}>
                  {a.path}
                </span>
              </div>

              {/* View indicator */}
              <Eye size={13} className="shrink-0" style={{ color: 'var(--text-muted)', opacity: 0.5 }} />
            </button>
          ))}

          {artifacts.length === 0 && (
            <div className="text-xs text-center py-8" style={{ color: 'var(--text-muted)' }}>
              No files generated yet
            </div>
          )}
        </div>
      </div>

      {/* ===== Full-screen Overlay Modal ===== */}
      {overlayPath && overlayArtifact && (
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
              {/* File icon + name */}
              <div
                className="flex items-center justify-center w-8 h-8 rounded"
                style={{ background: overlayMediaKind === 'video' ? 'rgba(244, 63, 94, 0.12)' : 'rgba(34, 197, 94, 0.12)' }}
              >
                <ArtifactIcon toolName={overlayArtifact.toolName} fileType={overlayArtifact.fileType} size={16} />
              </div>
              <div className="flex flex-col min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                    {overlayArtifact.fileName}
                  </span>
                  <span
                    className="text-[10px] px-1.5 py-0.5 rounded-full font-medium"
                    style={fileTypeBadgeStyle(overlayArtifact.fileType)}
                  >
                    {overlayArtifact.fileType}
                  </span>
                </div>
                <span className="text-[10px] truncate" style={{ color: 'var(--text-muted)' }} title={overlayArtifact.path}>
                  {overlayArtifact.path}
                </span>
              </div>

              {/* Download dropdown */}
              <div className="relative" ref={dropdownRef}>
                <button
                  onClick={() => setDownloadOpen(!downloadOpen)}
                  className="flex items-center gap-1.5 px-3 py-1.5 rounded text-xs font-medium"
                  style={{
                    background: 'var(--bg-primary)',
                    color: 'var(--text-secondary)',
                    border: '1px solid var(--border-color)',
                  }}
                  disabled={!!exporting}
                >
                  {exporting ? <Loader size={12} className="animate-spin" /> : <Download size={12} />}
                  <span>Download</span>
                  <ChevronDown size={10} />
                </button>
                {downloadOpen && (
                  <div
                    className="absolute right-0 top-full mt-1 py-1 rounded-md shadow-lg z-50 text-xs"
                    style={{
                      background: 'var(--bg-secondary)',
                      border: '1px solid var(--border-color)',
                      minWidth: '200px',
                    }}
                  >
                    <button
                      onClick={handleDownloadFile}
                      className="w-full text-left px-3 py-2 hover:opacity-80 transition-opacity"
                      style={{ color: 'var(--text-primary)' }}
                    >
                      {overlayDownloadLabel}
                    </button>
                    {isMarkdown && (
                      <>
                        <button
                          onClick={handleExportDOCX}
                          className="w-full text-left px-3 py-2 hover:opacity-80 transition-opacity"
                          style={{ color: 'var(--text-primary)' }}
                        >
                          Download as DOCX
                        </button>
                        <button
                          onClick={handleExportPDF}
                          className="w-full text-left px-3 py-2 hover:opacity-80 transition-opacity"
                          style={{ color: 'var(--text-primary)' }}
                        >
                          Download as PDF
                        </button>
                      </>
                    )}
                  </div>
                )}
              </div>

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
            <div className="flex-1 overflow-y-auto p-6">
              {overlayMediaKind ? (
                <div ref={contentRef} className="max-w-4xl mx-auto">
                  <ArtifactMediaPlayer
                    path={overlayArtifact.path}
                    fileName={overlayArtifact.fileName}
                    kind={overlayMediaKind}
                    sessionId={sessionId}
                  />
                </div>
              ) : (
                <>
                  {loading && (
                    <div className="flex items-center justify-center py-16">
                      <Loader size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
                    </div>
                  )}
                  {error && (
                    <div className="text-sm p-4 rounded-lg" style={{ color: '#f87171', background: 'rgba(248, 113, 113, 0.08)' }}>
                      Failed to load file: {error}
                    </div>
                  )}
                  {!loading && !error && content && (
                    <div ref={contentRef} className="max-w-4xl mx-auto">
                      {isMarkdown ? (
                        <div className="markdown-body markdown-body--document">
                          <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{content}</ReactMarkdown>
                        </div>
                      ) : (
                        <pre
                          className="text-sm whitespace-pre-wrap break-words p-4 rounded-lg"
                          style={{
                            color: 'var(--text-primary)',
                            fontFamily: 'var(--font-mono, monospace)',
                            background: 'var(--bg-secondary)',
                            border: '1px solid var(--border-color)',
                          }}
                        >
                          {content}
                        </pre>
                      )}
                    </div>
                  )}
                  {!loading && !error && !content && (
                    <div className="text-sm text-center py-16" style={{ color: 'var(--text-muted)' }}>
                      File is empty
                    </div>
                  )}
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  )
}
