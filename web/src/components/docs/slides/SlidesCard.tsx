import { useCallback, useEffect, useRef, useState } from 'react'
import { Download, ExternalLink, Loader2, Presentation, TriangleAlert } from 'lucide-react'

import { exportSlidesDeck, fetchSlidesPresentation, slidesPresentationURL, type DocsScope, type SlidesExportFormat } from '@/api/slides'
import { slidesHarnessLabel, type DocsUpdateMessage } from '@/components/chat/chatTypes'

interface SlidesCardProps {
  update: DocsUpdateMessage
  scope?: DocsScope
}

function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  URL.revokeObjectURL(url)
}

function slidesExportBasename(update: DocsUpdateMessage): string {
  const raw = (update.deckTitle || update.title || update.description || 'presentation').trim()
  const slug = raw.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60)
  return slug || 'presentation'
}

export default function SlidesCard({ update, scope = 'personal' }: SlidesCardProps) {
  const [pendingAction, setPendingAction] = useState<'present' | SlidesExportFormat | null>(null)
  const [error, setError] = useState('')
  const presentationUrlRef = useRef<string | null>(null)
  const presentationRequestRef = useRef(0)
  const mountedRef = useRef(true)
  const slideNumber = update.slideIndex
  const progress = slideNumber !== undefined && update.totalSlides !== undefined
    ? `${slideNumber} / ${update.totalSlides}`
    : update.totalSlides !== undefined
      ? `${update.totalSlides} slides`
      : 'Preparing deck'
  const title = slidesHarnessLabel(update)

  const loadPresentation = useCallback(async (): Promise<string | null> => {
    const requestId = ++presentationRequestRef.current
    const previousUrl = presentationUrlRef.current
    presentationUrlRef.current = null
    if (previousUrl) URL.revokeObjectURL(previousUrl)
    setError('')

    try {
      const blob = await fetchSlidesPresentation(update.deckSlug, scope)
      if (!mountedRef.current || requestId !== presentationRequestRef.current) return null

      const url = URL.createObjectURL(blob)
      presentationUrlRef.current = url
      return url
    } catch (cause) {
      if (mountedRef.current && requestId === presentationRequestRef.current) {
        setError(cause instanceof Error ? cause.message : 'Failed to load slide presentation')
      }
      return null
    }
  }, [scope, update.deckSlug])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      presentationRequestRef.current += 1
      if (presentationUrlRef.current) {
        URL.revokeObjectURL(presentationUrlRef.current)
        presentationUrlRef.current = null
      }
    }
  }, [])

  useEffect(() => {
    void loadPresentation()
    return () => {
      presentationRequestRef.current += 1
    }
  }, [loadPresentation, update.action, update.slideIndex, update.totalSlides])

  const present = async () => {
    setPendingAction('present')
    setError('')
    try {
      const url = presentationUrlRef.current ?? await loadPresentation()
      if (url && mountedRef.current) {
        window.open(slidesPresentationURL(update.deckSlug, scope, true), '_blank', 'noopener,noreferrer')
      }
    } finally {
      if (mountedRef.current) setPendingAction(null)
    }
  }

  const exportDeck = async (format: SlidesExportFormat) => {
    setPendingAction(format)
    setError('')
    try {
      const blob = await exportSlidesDeck(update.deckSlug, format, scope)
      downloadBlob(blob, `${slidesExportBasename(update)}.${format}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Failed to export ${format.toUpperCase()}`)
    } finally {
      setPendingAction(null)
    }
  }

  return (
    <section
      data-testid="slides-card"
      className="my-3 overflow-hidden rounded-xl border"
      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}
      aria-label={`Slide deck: ${title}`}
    >
      <div className="flex items-start justify-between gap-3 p-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="mt-0.5 rounded-lg p-2" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
            <Presentation size={18} aria-hidden="true" />
          </div>
          <div className="min-w-0">
            <h3 className="truncate text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>{title}</h3>
            <p className="mt-0.5 text-xs" style={{ color: 'var(--text-muted)' }}>{progress}</p>
          </div>
        </div>
        <span className="shrink-0 rounded-full px-2 py-1 text-[11px] font-medium" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
          {update.action === 'deck_created' ? 'Created' : 'Updated'}
        </span>
      </div>

      {(update.validation || update.pptxCapability) && (
        <div className="grid grid-cols-1 gap-2 border-t px-4 py-3 text-xs sm:grid-cols-2" style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}>
          {update.validation && (
            <span>Validation: {update.validation.errors} errors, {update.validation.warnings} warnings</span>
          )}
          {update.pptxCapability && (
            <span>PPTX: {update.pptxCapability.native} native, {update.pptxCapability.unsupported} unsupported</span>
          )}
        </div>
      )}

      {error && (
        <div className="flex items-start gap-2 border-t px-4 py-2 text-xs text-red-400" style={{ borderColor: 'var(--border-color)' }} role="alert">
          <TriangleAlert size={14} className="mt-0.5 shrink-0" aria-hidden="true" />
          <span>{error}</span>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2 border-t px-4 py-3" style={{ borderColor: 'var(--border-color)' }}>
        <button type="button" onClick={present} disabled={pendingAction !== null} className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium disabled:opacity-50" style={{ background: 'var(--brand)', color: 'var(--brand-foreground)' }}>
          {pendingAction === 'present' ? <Loader2 size={13} className="animate-spin" /> : <ExternalLink size={13} />}
          Present
        </button>
        {(['pptx', 'pdf', 'html'] as SlidesExportFormat[]).map(format => (
          <button key={format} type="button" onClick={() => exportDeck(format)} disabled={pendingAction !== null} className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium disabled:opacity-50" style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}>
            {pendingAction === format ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}
            {format.toUpperCase()}
          </button>
        ))}
      </div>
    </section>
  )
}
