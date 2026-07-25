import { useState, useEffect, useRef, useCallback } from 'react'
import { Maximize2, ChevronDown, FileText, Download, Loader, FilePlus, Edit3, Film } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './markdownComponents'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import { fetchArtifactContent, getArtifactDownloadUrl, getArtifactPDFUrl } from '../../api/studioChat'
import { artifactMediaKind, downloadLabelForArtifact } from '../../utils/artifactMedia'
import ArtifactMediaPlayer from './ArtifactMediaPlayer'
import type { SessionArtifact } from './chatTypes'

// File type badge color mapping (same as FilePanel)
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

// Embedded file viewer for a single artifact — renders the file content inline
// with a header bar (filename, type badge, download dropdown, fullscreen button).
// Used inside agent message bubbles for markdown reports and last-turn media.
export default function EmbeddedFileViewer({ artifact, sessionId, onOpenInPanel, fillHeight = false }: {
  artifact: SessionArtifact
  sessionId?: string | null
  onOpenInPanel?: (path: string) => void
  /** When true, fill the parent height instead of capping content at 500px (harness panel). */
  fillHeight?: boolean
}) {
  const [content, setContent] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [downloadOpen, setDownloadOpen] = useState(false)
  const [exporting, setExporting] = useState<string | null>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const isMarkdown = artifact.fileType === 'Markdown'
  const mediaKind = artifactMediaKind(artifact.fileType, artifact.fileName)
  const isEdit = artifact.toolName === 'edit_file'
  const downloadLabel = downloadLabelForArtifact(artifact.fileType, artifact.fileName)

  // Load text content only for non-media files (media uses ArtifactMediaPlayer).
  useEffect(() => {
    if (mediaKind) {
      setContent('')
      setLoading(false)
      setError(null)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    fetchArtifactContent(artifact.path, sessionId || undefined)
      .then(text => { if (!cancelled) setContent(text) })
      .catch(err => { if (!cancelled) setError(err.message) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [artifact.path, sessionId, mediaKind])

  // Close dropdown on outside click
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

  const handleDownloadFile = useCallback(() => {
    const url = getArtifactDownloadUrl(artifact.path, sessionId || undefined)
    const a = document.createElement('a')
    a.href = url
    a.download = artifact.fileName
    a.click()
    setDownloadOpen(false)
  }, [artifact.path, artifact.fileName, sessionId])

  const handleExportPDF = useCallback(async () => {
    setDownloadOpen(false)
    setExporting('pdf')
    try {
      const url = getArtifactPDFUrl(artifact.path, sessionId || undefined)
      const res = await fetch(url)
      if (!res.ok) throw new Error(`PDF generation failed: ${res.statusText}`)
      const blob = await res.blob()
      const { saveAs } = await import('file-saver')
      saveAs(blob, artifact.fileName.replace(/\.[^.]+$/, '.pdf'))
    } catch (err) {
      console.error('PDF export failed:', err)
    } finally {
      setExporting(null)
    }
  }, [artifact.path, artifact.fileName, sessionId])

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
      saveAs(blob, artifact.fileName.replace(/\.[^.]+$/, '.docx'))
    } catch (err) {
      console.error('DOCX export failed:', err)
    } finally {
      setExporting(null)
    }
  }, [content, artifact.fileName])

  return (
    <div
      className={fillHeight ? 'rounded-lg overflow-hidden h-full flex flex-col min-h-0' : 'rounded-lg overflow-hidden'}
      style={{
        border: '1px solid var(--border-color)',
        background: 'var(--bg-primary)',
      }}
    >
      {/* File header bar */}
      <div
        className="flex items-center gap-2.5 px-3 py-2"
        style={{
          borderBottom: '1px solid var(--border-color)',
          background: 'var(--bg-secondary)',
        }}
      >
        {/* File icon */}
        <div
          className="flex items-center justify-center w-7 h-7 rounded shrink-0"
          style={{ background: mediaKind === 'video' ? 'rgba(244, 63, 94, 0.12)' : 'rgba(34, 197, 94, 0.12)' }}
        >
          {mediaKind === 'video'
            ? <Film size={14} className="text-rose-400" />
            : isEdit
              ? <Edit3 size={14} className="text-green-400" />
              : <FilePlus size={14} className="text-green-400" />
          }
        </div>

        {/* Filename + type badge. When the artifact carries a reportTitle
            (set by an `astonish-report` fence's frontmatter), display that as
            the human-readable label and demote the basename to a tooltip.
            Falls back to the basename when no title is available. */}
        <div className="flex items-center gap-2 min-w-0 flex-1">
          {mediaKind === 'video'
            ? <Film size={13} className="text-rose-400 shrink-0" />
            : <FileText size={13} className="text-green-400 shrink-0" />
          }
          <span
            className="text-xs font-medium truncate"
            style={{ color: 'var(--text-primary)' }}
            title={artifact.fileName}
          >
            {artifact.reportTitle || artifact.fileName}
          </span>
          <span
            className="text-[10px] px-1.5 py-0.5 rounded-full shrink-0 font-medium"
            style={fileTypeBadgeStyle(artifact.fileType)}
          >
            {artifact.fileType}
          </span>
        </div>

        {/* Download dropdown */}
        <div className="relative shrink-0" ref={dropdownRef}>
          <button
            onClick={() => setDownloadOpen(!downloadOpen)}
            className="flex items-center gap-1 px-2 py-1 rounded text-[10px] font-medium hover:opacity-80 transition-opacity"
            style={{
              background: 'var(--bg-primary)',
              color: 'var(--text-secondary)',
              border: '1px solid var(--border-color)',
            }}
            disabled={!!exporting}
          >
            {exporting ? <Loader size={10} className="animate-spin" /> : <Download size={10} />}
            <span>Download</span>
            <ChevronDown size={8} />
          </button>
          {downloadOpen && (
            <div
              className="absolute right-0 top-full mt-1 py-1 rounded-md shadow-lg z-50 text-xs"
              style={{
                background: 'var(--bg-secondary)',
                border: '1px solid var(--border-color)',
                minWidth: '180px',
              }}
            >
              <button
                onClick={handleDownloadFile}
                className="w-full text-left px-3 py-1.5 hover:opacity-80 transition-opacity"
                style={{ color: 'var(--text-primary)' }}
              >
                {downloadLabel}
              </button>
              {isMarkdown && (
                <>
                  <button
                    onClick={handleExportDOCX}
                    className="w-full text-left px-3 py-1.5 hover:opacity-80 transition-opacity"
                    style={{ color: 'var(--text-primary)' }}
                  >
                    Download as DOCX
                  </button>
                  <button
                    onClick={handleExportPDF}
                    className="w-full text-left px-3 py-1.5 hover:opacity-80 transition-opacity"
                    style={{ color: 'var(--text-primary)' }}
                  >
                    Download as PDF
                  </button>
                </>
              )}
            </div>
          )}
        </div>

        {/* Fullscreen button — opens in FilePanel overlay */}
        {onOpenInPanel && (
          <button
            onClick={() => onOpenInPanel(artifact.path)}
            className="p-1 rounded hover:bg-white/10 transition-colors shrink-0"
            title="Open fullscreen"
            style={{ color: 'var(--text-muted)' }}
          >
            <Maximize2 size={13} />
          </button>
        )}
      </div>

      {/* File content */}
      <div
        className={fillHeight ? 'flex-1 min-h-0 overflow-y-auto p-4' : 'overflow-y-auto p-4'}
        style={fillHeight || mediaKind === 'video' ? undefined : { maxHeight: '500px' }}
      >
        {mediaKind ? (
          <ArtifactMediaPlayer
            path={artifact.path}
            fileName={artifact.fileName}
            kind={mediaKind}
            sessionId={sessionId}
          />
        ) : (
          <>
            {loading && (
              <div className="flex items-center justify-center py-8">
                <Loader size={18} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
              </div>
            )}
            {error && (
              <div className="text-xs p-3 rounded" style={{ color: '#f87171', background: 'rgba(248, 113, 113, 0.08)' }}>
                Failed to load file: {error}
              </div>
            )}
            {!loading && !error && content && (
              <div className="max-w-4xl">
                {isMarkdown ? (
                  <div className="markdown-body markdown-body--document text-sm">
                    <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{content}</ReactMarkdown>
                  </div>
                ) : (
                  <pre
                    className="text-xs whitespace-pre-wrap break-words p-3 rounded"
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
              <div className="text-xs text-center py-6" style={{ color: 'var(--text-muted)' }}>
                File is empty
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
