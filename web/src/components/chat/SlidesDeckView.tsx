import { useCallback, useEffect, useRef, useState } from 'react'
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
  /**
   * Monotonic signal that bumps whenever new slides may have been written for
   * this deck (e.g. each `docs_update` in the chat harness). A change forces a
   * re-fetch of the deck and re-mounts the embedded present iframe so freshly
   * generated slides appear without a manual page reload.
   */
  refreshSignal?: number
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
export default function SlidesDeckView({ deckSlug, scope = 'personal', fillHeight = false, refreshSignal = 0 }: SlidesDeckViewProps) {
  const [deck, setDeck] = useState<SlidesDeckResponse | null>(null)
  const [slideIndex, setSlideIndex] = useState(0)
  const [error, setError] = useState('')
  const [pendingExport, setPendingExport] = useState<SlidesExportFormat | null>(null)
  const [fullscreen, setFullscreen] = useState(false)
  const mountedRef = useRef(true)
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const fsIframeRef = useRef<HTMLIFrameElement | null>(null)
  // Tracks the deck/scope this component last loaded, so a pure refreshSignal
  // bump (same deck gaining slides) re-fetches WITHOUT yanking the user off
  // whatever slide they're viewing — we only reset slideIndex when the deck or
  // scope actually changes.
  const loadedKeyRef = useRef<string | null>(null)
  // Slide count the embedded iframe was last (re)loaded with. New slides only
  // require an iframe reload when the count actually grew; navigation and other
  // docs_update churn must NOT reload the document (that caused the flicker).
  const renderedCountRef = useRef(0)

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  useEffect(() => {
    let cancelled = false
    const key = `${deckSlug}|${scope}`
    if (loadedKeyRef.current !== key) {
      // New deck/scope: reset navigation to the first slide.
      setSlideIndex(0)
      loadedKeyRef.current = key
      renderedCountRef.current = 0
    }
    setError('')
    fetchSlidesDeck(deckSlug, scope)
      .then(response => { if (!cancelled) setDeck(response) })
      .catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : 'Failed to load slide deck') })
    return () => { cancelled = true }
  }, [deckSlug, scope, refreshSignal])

  const slides = deck?.slides ?? []
  const total = slides.length
  const boundedIndex = Math.min(slideIndex, Math.max(0, total - 1))
  const presentUrl = slidesPresentationURL(deckSlug, scope)
  // The iframe mounts ONCE per deck/scope (key excludes slideIndex + refreshSignal).
  // We load the deck at its first slide; subsequent navigation and live slide
  // additions are driven imperatively (postMessage nav + a targeted reload when
  // the count grows) so the user never sees a full-document flash.
  const iframeSrc = presentUrl

  // Navigate the embedded deck to boundedIndex WITHOUT reloading it. The runtime
  // (AstDeck) listens for { type: 'ast-nav', index } on the opaque-origin iframe
  // and calls DeckController.goTo. targetOrigin is '*' because the sandboxed
  // iframe has an opaque origin; the payload is inert (a slide index).
  const postNav = useCallback((index: number) => {
    const win = (fullscreen ? fsIframeRef.current : iframeRef.current)?.contentWindow
    win?.postMessage({ type: 'ast-nav', index }, '*')
  }, [fullscreen])

  // Drive navigation on thumbnail click / index change — no remount, no reload.
  useEffect(() => {
    if (total === 0) return
    postNav(boundedIndex)
  }, [boundedIndex, total, postNav])

  // Live slide additions: reload the embedded document only when the slide count
  // actually increased (a new write_slide landed), then restore the viewed
  // slide. Pure docs_update churn (validation/review events) never reloads.
  useEffect(() => {
    if (total > renderedCountRef.current) {
      renderedCountRef.current = total
      const frame = fullscreen ? fsIframeRef.current : iframeRef.current
      if (frame) {
        const onLoad = () => { postNav(boundedIndex) }
        frame.addEventListener('load', onLoad, { once: true })
        // Reassigning src reloads the (single) iframe in place — far less jarring
        // than React unmounting/remounting the element, and it keeps focus/scroll.
        frame.src = iframeSrc
      }
    }
  // boundedIndex intentionally read at reload time only; excluded from deps so a
  // mere navigation does not trigger a reload.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [total, iframeSrc, fullscreen, postNav])

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

  const deckTitle = deck?.deck.title || deckSlug
  // The embedded deck iframe is mounted ONCE per deck/scope. Navigation and live
  // slide additions are driven imperatively (see effects above) so we never
  // change the element's `key` on nav/refresh — that remount was the flicker.
  const deckFrame = (
    <iframe
      ref={iframeRef}
      key={`${deckSlug}|${scope}`}
      src={iframeSrc}
      sandbox="allow-scripts"
      title={`Slide deck: ${deckTitle}`}
      data-testid="slides-deck-frame"
      className="h-full w-full rounded-lg border-0"
      style={{ background: 'var(--card)' }}
    />
  )

  // Shown in the deck area while the deck exists but has no slides yet — the
  // panel reveals from the start (see chatHarness.deriveLatestHarness) so the
  // user sees this instead of an empty/broken deck until the first slide lands.
  const generatingPlaceholder = (
    <div
      data-testid="slides-generating"
      className="flex h-full w-full flex-col items-center justify-center gap-3 rounded-lg text-center"
      style={{ background: 'var(--card)', border: '1px dashed var(--border-color)' }}
    >
      <Loader2 size={22} className="animate-spin" style={{ color: 'var(--brand)' }} />
      <div className="text-sm font-medium text-foreground">Generating slides…</div>
      <div className="max-w-xs px-6 text-xs text-muted-foreground">
        The deck is being created. Slides will appear here as they’re written — no need to reload.
      </div>
    </div>
  )

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

      {/* Embedded deck — the iframe stays mounted; while the deck has no slides
          yet the "generating" placeholder covers it so the user never sees an
          empty document. */}
      <div className={cn('relative min-h-0 overflow-hidden rounded-lg', fillHeight ? 'flex-1' : 'aspect-video')}>
        {deckFrame}
        {total === 0 && (
          <div className="absolute inset-0">
            {generatingPlaceholder}
          </div>
        )}
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
                ref={fsIframeRef}
                key={`fullscreen-${deckSlug}|${scope}`}
                src={iframeSrc}
                sandbox="allow-scripts"
                title={`Slide deck full screen: ${deckTitle}`}
                onLoad={() => { if (total > 0) postNav(boundedIndex) }}
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
