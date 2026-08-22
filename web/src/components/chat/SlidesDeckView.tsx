import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Download, ExternalLink, Loader2, Maximize2, TriangleAlert, X } from 'lucide-react'

import {
  exportSlidesDeck,
  fetchSlidesDeck,
  slidesPresentationURL,
  type DocsScope,
  type SlidesDeckResponse,
  type SlidesExportFormat,
} from '@/api/slides'
import { cn } from '@/lib/utils'

interface SlidesDeckViewProps {
  deckSlug: string
  scope?: DocsScope
  /** Fill the parent height (harness panel) instead of a fixed aspect box. */
  fillHeight?: boolean
}

/**
 * Renders a stored slide deck inside the harness panel by embedding the
 * server-rendered, self-contained present document in a sandboxed iframe
 * (same transport used by the Present tab, so the themed background matches).
 * A slide strip navigates the embedded deck via positional `#slide-<n>` hashes
 * (see DeckController.indexFromLocation), which is stable regardless of
 * author-set markup ids. Present/export affordances live here in the panel —
 * the in-chat card is only a compact launcher.
 */
export default function SlidesDeckView({ deckSlug, scope = 'personal', fillHeight = false }: SlidesDeckViewProps) {
  const [deck, setDeck] = useState<SlidesDeckResponse | null>(null)
  const [slideIndex, setSlideIndex] = useState(0)
  const [error, setError] = useState('')
  const [pendingExport, setPendingExport] = useState<SlidesExportFormat | null>(null)
  const [fullscreen, setFullscreen] = useState(false)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => {
    let cancelled = false
    setError('')
    setSlideIndex(0)
    fetchSlidesDeck(deckSlug, scope)
      .then(response => { if (!cancelled) setDeck(response) })
      .catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : 'Failed to load slide deck') })
    return () => { cancelled = true }
  }, [deckSlug, scope])

  const slides = deck?.slides ?? []
  const total = slides.length
  const boundedIndex = Math.min(slideIndex, Math.max(0, total - 1))
  // Positional 1-based hash — DeckController resolves `#slide-<n>` via data-index.
  const slideHash = `slide-${boundedIndex + 1}`
  const presentUrl = slidesPresentationURL(deckSlug, scope)
  const iframeSrc = total > 0 ? `${presentUrl}#${slideHash}` : presentUrl

  const present = useCallback(() => {
    window.open(slidesPresentationURL(deckSlug, scope), '_blank', 'noopener,noreferrer')
  }, [deckSlug, scope])

  const exportDeck = useCallback(async (format: SlidesExportFormat) => {
    setPendingExport(format)
    setError('')
    try {
      const blob = await exportSlidesDeck(deckSlug, format, scope)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${deckSlug}.${format}`
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (cause) {
      if (mountedRef.current) setError(cause instanceof Error ? cause.message : `Failed to export ${format.toUpperCase()}`)
    } finally {
      if (mountedRef.current) setPendingExport(null)
    }
  }, [deckSlug, scope])

  useEffect(() => {
    if (!fullscreen) return
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') setFullscreen(false) }
    document.addEventListener('keydown', handler)
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', handler)
      document.body.style.overflow = previousOverflow
    }
  }, [fullscreen])

  const deckFrame = useMemo(() => (
    <iframe
      key={`${deckSlug}#${slideHash}`}
      src={iframeSrc}
      sandbox="allow-scripts"
      title={`Slide deck: ${deck?.deck.title || deckSlug}`}
      data-testid="slides-deck-frame"
      className="h-full w-full rounded-lg border-0"
      style={{ background: 'var(--card)' }}
    />
  ), [deckSlug, slideHash, iframeSrc, deck?.deck.title])

  return (
    <div className={cn('flex flex-col gap-3', fillHeight ? 'h-full min-h-0' : '')}>
      {error && (
        <div
          className="flex items-start gap-2 rounded-md px-3 py-2 text-xs text-red-400"
          style={{ border: '1px solid var(--border-color)' }}
          role="alert"
        >
          <TriangleAlert size={14} className="mt-0.5 shrink-0" aria-hidden="true" />
          <span>{error}</span>
        </div>
      )}

      {/* Panel toolbar — Present + exports live here, not on the inline card */}
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={present}
          data-testid="slides-present"
          className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium"
          style={{ background: 'var(--brand)', color: 'var(--brand-foreground)' }}
        >
          <ExternalLink size={13} />
          Present
        </button>
        <button
          type="button"
          onClick={() => setFullscreen(true)}
          data-testid="slides-fullscreen"
          className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium"
          style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
        >
          <Maximize2 size={13} />
          Full screen
        </button>
        {(['pptx', 'pdf', 'html'] as SlidesExportFormat[]).map(format => (
          <button
            key={format}
            type="button"
            onClick={() => exportDeck(format)}
            disabled={pendingExport !== null}
            className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium disabled:opacity-50"
            style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
          >
            {pendingExport === format ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}
            {format.toUpperCase()}
          </button>
        ))}
      </div>

      {/* Embedded deck */}
      <div className={cn('min-h-0 overflow-hidden rounded-lg', fillHeight ? 'flex-1' : 'aspect-video')}>
        {deckFrame}
      </div>

      {/* Slide strip */}
      {total > 0 && (
        <div className="flex shrink-0 gap-2 overflow-x-auto pb-1" role="tablist" aria-label="Slides">
          {slides.map((slide, index) => (
            <button
              key={slide.id}
              type="button"
              role="tab"
              aria-selected={index === boundedIndex}
              aria-current={index === boundedIndex ? 'true' : undefined}
              aria-label={`Slide ${index + 1}${slide.title ? `: ${slide.title}` : ''}`}
              data-testid="slides-tile"
              onClick={() => setSlideIndex(index)}
              className={cn(
                'flex h-14 w-24 shrink-0 flex-col items-start justify-between rounded-md border px-2 py-1.5 text-left transition-colors',
                index === boundedIndex
                  ? 'border-primary bg-primary/10'
                  : 'border-border bg-card hover:border-primary/40'
              )}
            >
              <span className="text-[11px] font-semibold text-foreground">{index + 1}</span>
              {slide.title && (
                <span className="line-clamp-2 text-[10px] leading-tight text-muted-foreground">{slide.title}</span>
              )}
            </button>
          ))}
        </div>
      )}

      {/* Full-screen overlay (mirrors AppsPanel overlay pattern) */}
      {fullscreen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center"
          style={{ background: 'rgba(0, 0, 0, 0.6)', backdropFilter: 'blur(4px)' }}
          onClick={(e) => { if (e.target === e.currentTarget) setFullscreen(false) }}
        >
          <div
            className="flex flex-col overflow-hidden rounded-xl shadow-2xl"
            style={{ width: '90vw', height: '92vh', background: 'var(--bg-primary)', border: '1px solid var(--border-color)' }}
          >
            <div
              className="flex shrink-0 items-center justify-between px-4 py-2.5"
              style={{ borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
            >
              <span className="truncate text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                {deck?.deck.title || deckSlug}
              </span>
              <button
                type="button"
                onClick={() => setFullscreen(false)}
                className="rounded p-1.5 hover:opacity-70"
                style={{ color: 'var(--text-muted)' }}
                aria-label="Close full screen"
              >
                <X size={18} />
              </button>
            </div>
            <div className="min-h-0 flex-1">
              <iframe
                key={`fullscreen-${deckSlug}#${slideHash}`}
                src={iframeSrc}
                sandbox="allow-scripts"
                title={`Slide deck full screen: ${deck?.deck.title || deckSlug}`}
                className="h-full w-full border-0"
                style={{ background: 'var(--card)' }}
              />
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
