import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { Download, ExternalLink, Loader2, Maximize2, Save, Trash2, TriangleAlert, X } from 'lucide-react'

import {
  exportSlidesDeck,
  fetchSlidesDeck,
  patchSlideMoves,
  saveDeck,
  slideEditIsDirty,
  slidesPresentationURL,
  type DocsScope,
  type SlideEditDraft,
  type SlidesDeckResponse,
  type SlidesExportFormat,
} from '@/api/slides'
import { cn } from '@/lib/utils'
import SlidesArchetypeThumb from './questions/SlidesArchetypeThumb'

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
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [saveDialogOpen, setSaveDialogOpen] = useState(false)
  const [saveName, setSaveName] = useState('')
  const [fullscreen, setFullscreen] = useState(false)
  const [pendingBySlide, setPendingBySlide] = useState<Record<number, SlideEditDraft>>({})
  const [selectedObject, setSelectedObject] = useState<{ id: string; tag: string } | null>(null)
  const [applying, setApplying] = useState(false)
  const mountedRef = useRef(true)
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const fsIframeRef = useRef<HTMLIFrameElement | null>(null)
  const stripRef = useRef<HTMLDivElement | null>(null)
  // Tracks the deck/scope this component last loaded, so a pure refreshSignal
  // bump (same deck gaining slides) re-fetches WITHOUT yanking the user off
  // whatever slide they're viewing — we only reset slideIndex when the deck or
  // scope actually changes.
  const loadedKeyRef = useRef<string | null>(null)

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
    }
    setError('')
    fetchSlidesDeck(deckSlug, scope)
      .then(response => { if (!cancelled) setDeck(response) })
      .catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : 'Failed to load slide deck') })
    return () => { cancelled = true }
  }, [deckSlug, scope, refreshSignal])

  useEffect(() => {
    setPendingBySlide({})
    setSelectedObject(null)
  }, [deckSlug, scope, refreshSignal])

  const slides = deck?.slides ?? []
  const total = slides.length
  const boundedIndex = Math.min(slideIndex, Math.max(0, total - 1))
  const presentUrl = slidesPresentationURL(deckSlug, scope)
  // Cache-bust the present document on every refreshSignal. Reassigning the
  // same URL does not reload in browsers, so slide writes looked stale until
  // a full page refresh. Navigation still uses postMessage (src unchanged
  // while the token is stable).
  const iframeSrc = presentUrl.includes('?')
    ? `${presentUrl}&t=${refreshSignal}`
    : `${presentUrl}?t=${refreshSignal}`

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

  // Click/keyboard nav happens inside the sandboxed present iframe. Mirror
  // ast-deck-change messages onto the strip selection. Canvas edits arrive as
  // ast-edit-changed; selection as ast-edit-selected.
  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      const data = event.data as {
        type?: string
        index?: number
        id?: string | null
        tag?: string | null
        changes?: SlideEditDraft['moves']
        moves?: SlideEditDraft['moves']
        resizes?: SlideEditDraft['resizes']
        texts?: SlideEditDraft['texts']
        deletes?: string[]
      } | null
      if (!data?.type) return
      if (data.type === 'ast-deck-change') {
        const index = data.index
        if (typeof index !== 'number' || !Number.isInteger(index) || index < 0) return
        setSlideIndex(prev => (prev === index ? prev : index))
        return
      }
      if (data.type === 'ast-edit-selected') {
        const id = typeof data.id === 'string' && data.id ? data.id : null
        const tag = typeof data.tag === 'string' && data.tag ? data.tag : ''
        setSelectedObject(id ? { id, tag } : null)
        return
      }
      if (data.type === 'ast-edit-changed' || data.type === 'ast-edit-moved') {
        const index = data.index
        if (typeof index !== 'number' || !Number.isInteger(index) || index < 0) return
        const draft: SlideEditDraft = {
          moves: data.moves ?? data.changes ?? [],
          ...(data.resizes?.length ? { resizes: data.resizes } : {}),
          texts: data.texts ?? [],
          deletes: data.deletes ?? [],
        }
        setPendingBySlide(prev => {
          if (!slideEditIsDirty(draft)) {
            if (!(index in prev)) return prev
            const next = { ...prev }
            delete next[index]
            return next
          }
          return { ...prev, [index]: draft }
        })
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  const postToCanvas = useCallback((payload: Record<string, unknown>) => {
    iframeRef.current?.contentWindow?.postMessage(payload, '*')
  }, [])

  // After the present document reloads (new slides or an in-place rewrite),
  // restore the strip's current index inside the iframe and enable canvas edit.
  const onPresentLoad = useCallback(() => {
    if (total > 0) postNav(boundedIndex)
  }, [total, boundedIndex, postNav])

  const onCanvasLoad = useCallback(() => {
    onPresentLoad()
    postToCanvas({ type: 'ast-edit-mode', enabled: true })
  }, [onPresentLoad, postToCanvas])

  const currentPending = pendingBySlide[boundedIndex]
  const editDirty = slideEditIsDirty(currentPending) && !fullscreen
  const canDeleteObject = Boolean(selectedObject) && !fullscreen && !applying

  const discardEdits = useCallback(() => {
    postToCanvas({ type: 'ast-edit-reset' })
    setSelectedObject(null)
    setPendingBySlide(prev => {
      if (!(boundedIndex in prev)) return prev
      const next = { ...prev }
      delete next[boundedIndex]
      return next
    })
  }, [boundedIndex, postToCanvas])

  const applyEdits = useCallback(async () => {
    if (!slideEditIsDirty(currentPending) || !currentPending) return
    setApplying(true)
    setError('')
    try {
      const updated = await patchSlideMoves(deckSlug, boundedIndex, currentPending, scope)
      setDeck(prev => {
        if (!prev) return prev
        return {
          ...prev,
          slides: prev.slides.map((slide, i) => i === boundedIndex ? { ...slide, content: updated.content } : slide),
        }
      })
      postToCanvas({ type: 'ast-edit-commit' })
      setSelectedObject(null)
      setPendingBySlide(prev => {
        const next = { ...prev }
        delete next[boundedIndex]
        return next
      })
    } catch (cause) {
      if (mountedRef.current) setError(cause instanceof Error ? cause.message : 'Failed to save slide layout')
    } finally {
      if (mountedRef.current) setApplying(false)
    }
  }, [boundedIndex, currentPending, deckSlug, postToCanvas, scope])

  const focusStripTile = useCallback((index: number) => {
    const tiles = stripRef.current?.querySelectorAll<HTMLButtonElement>('[data-testid="slides-tile"]')
    const tile = tiles?.[index]
    if (!tile) return
    tile.focus()
    tile.scrollIntoView({ inline: 'nearest', block: 'nearest' })
  }, [])

  const onStripTileKeyDown = useCallback((event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    if (total <= 0) return
    let next = index
    switch (event.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        next = Math.min(total - 1, index + 1)
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        next = Math.max(0, index - 1)
        break
      case 'Home':
        next = 0
        break
      case 'End':
        next = total - 1
        break
      default:
        return
    }
    event.preventDefault()
    if (next === index) return
    setSlideIndex(next)
    requestAnimationFrame(() => focusStripTile(next))
  }, [total, focusStripTile])

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

  const handleSave = useCallback(async (title: string) => {
    // Derive a URL-safe slug from the title (prefix with "saved-" to avoid session-deck slug collisions)
    const slug = 'saved-' + title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 60)
    setSaving(true)
    setError('')
    try {
      await saveDeck(deckSlug, { targetSlug: slug, title }, scope)
      setSaveSuccess(true)
      setTimeout(() => { if (mountedRef.current) setSaveSuccess(false) }, 3000)
      window.dispatchEvent(new CustomEvent('astonish:slides-updated'))
    } catch (cause) {
      if (mountedRef.current) setError(cause instanceof Error ? cause.message : 'Failed to save deck')
    } finally {
      if (mountedRef.current) setSaving(false)
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
      onLoad={onCanvasLoad}
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

        {canDeleteObject ? (
          <button
            type="button"
            onClick={() => postToCanvas({ type: 'ast-edit-delete' })}
            data-testid="slides-edit-delete"
            className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium"
            style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
          >
            <Trash2 size={13} />
            Delete
          </button>
        ) : null}

        {/* Right slot: Save, or Discard/Apply while a canvas edit is pending. */}
        {editDirty ? (
          <div className="ml-auto flex items-center gap-1.5">
            <button
              type="button"
              onClick={discardEdits}
              disabled={applying}
              data-testid="slides-edit-discard"
              className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium disabled:opacity-50"
              style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)' }}
            >
              Discard
            </button>
            <button
              type="button"
              onClick={() => { void applyEdits() }}
              disabled={applying}
              data-testid="slides-edit-apply"
              className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50"
              style={{ background: 'var(--brand)' }}
            >
              {applying ? <Loader2 size={13} className="animate-spin" /> : null}
              Apply
            </button>
          </div>
        ) : saving ? (
          <div className="ml-auto flex items-center gap-1.5">
            <span className="inline-flex items-center gap-1.5 text-xs" style={{ color: 'var(--text-muted)' }}>
              <Loader2 size={13} className="animate-spin" /> Saving…
            </span>
          </div>
        ) : saveSuccess ? (
          <div className="ml-auto flex items-center gap-1.5">
            <span className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium"
              style={{ color: 'var(--success, #10b981)' }}>
              ✓ Saved!
            </span>
          </div>
        ) : deck?.deck.sessionId ? (
          <div className="ml-auto flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => { setSaveName(deck?.deck.title || ''); setSaveDialogOpen(true) }}
              disabled={saving}
              data-testid="slides-save"
              className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50"
              style={{ background: 'var(--brand)' }}
            >
              <Save size={13} />
              Save
            </button>
          </div>
        ) : null}
      </div>

      {/* Save dialog — inline modal for naming the deck */}
      {saveDialogOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center"
          style={{ background: 'rgba(0,0,0,0.5)' }}
          onClick={e => { if (e.target === e.currentTarget) setSaveDialogOpen(false) }}
        >
          <div
            className="mx-4 w-full max-w-sm rounded-xl p-5"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}
          >
            <h3 className="mb-1 text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
              Save Slide Deck
            </h3>
            <p className="mb-3 text-xs" style={{ color: 'var(--text-muted)' }}>
              Choose a name for your deck. Saving again with the same name creates a new version.
            </p>
            <input
              type="text"
              autoFocus
              value={saveName}
              onChange={e => setSaveName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && saveName.trim()) { setSaveDialogOpen(false); handleSave(saveName.trim()) } }}
              placeholder="My Presentation"
              className="mb-3 w-full rounded-md border px-3 py-2 text-sm outline-none"
              style={{ borderColor: 'var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
              data-testid="slides-save-name-input"
            />
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setSaveDialogOpen(false)}
                className="cursor-pointer rounded px-3 py-1.5 text-xs"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
              >
                Cancel
              </button>
              <button
                onClick={() => { if (saveName.trim()) { setSaveDialogOpen(false); handleSave(saveName.trim()) } }}
                disabled={!saveName.trim()}
                className="cursor-pointer rounded px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50"
                style={{ background: 'var(--brand)' }}
                data-testid="slides-save-confirm"
              >
                Save
              </button>
            </div>
          </div>
        </div>
      )}

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

      {/* Slide strip. Padding keeps the selected ring inside the scrollport
          (overflow-x:auto otherwise clips it on the top and left). The preview
          is clipped in an inner layer so the ring on the button is not. */}
      {total > 0 && (
        <div
          ref={stripRef}
          className="relative z-10 flex shrink-0 gap-2 overflow-x-auto px-1 pt-1.5 pb-1.5"
          role="tablist"
          aria-label="Slides"
        >
          {slides.map((slide, index) => (
            <button
              key={slide.id}
              type="button"
              role="tab"
              tabIndex={index === boundedIndex ? 0 : -1}
              aria-selected={index === boundedIndex}
              aria-current={index === boundedIndex ? 'true' : undefined}
              aria-label={`Slide ${index + 1}${slide.title ? `: ${slide.title}` : ''}`}
              data-testid="slides-tile"
              onClick={() => setSlideIndex(index)}
              onKeyDown={event => onStripTileKeyDown(event, index)}
              className={cn(
                'relative h-14 w-24 shrink-0 rounded-md border text-left transition-colors',
                index === boundedIndex
                  ? 'z-10 border-primary ring-2 ring-primary'
                  : 'border-border bg-card hover:border-primary/40'
              )}
            >
              <span className="pointer-events-none absolute inset-0 overflow-hidden rounded-[5px]">
                {slide.content ? (
                  <SlidesArchetypeThumb
                    markup={slide.content}
                    theme={deck?.deck.theme}
                    template={deck?.deck.theme?.['template-name']}
                  />
                ) : (
                  <span className="flex h-full w-full items-center justify-center text-[11px] font-semibold text-muted-foreground">
                    {index + 1}
                  </span>
                )}
              </span>
              <span className="pointer-events-none absolute left-1 top-0.5 z-10 rounded bg-black/50 px-1 text-[10px] font-semibold text-white">
                {index + 1}
              </span>
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
                onLoad={onPresentLoad}
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
