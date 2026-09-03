import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { Download, ExternalLink, Loader2, Maximize2, Save, Trash2, TriangleAlert, X, ChevronLeft, ChevronRight } from 'lucide-react'

import {
  exportSlidesDeck,
  fetchSlidesDeck,
  patchSlideMoves,
  saveDeck,
  slideEditIsDirty,
  slidesPresentationURL,
  uploadSlideAsset,
  type DocsScope,
  type SlideEditDraft,
  type SlidesDeckResponse,
  type SlidesExportFormat,
} from '@/api/slides'
import type { SelectionMetadata } from '@/components/docs/slides/runtime/EditController'
import ShapeToolbar, { SHAPE_DEFAULTS } from '@/components/slides/ShapeToolbar'
import ElementPropertiesPanel from '@/components/slides/ElementPropertiesPanel'
import TextFormatBar from '@/components/slides/TextFormatBar'
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
  const [selectedObject, setSelectedObject] = useState<SelectionMetadata | null>(null)
  const [applying, setApplying] = useState(false)
  const [activeTool, setActiveTool] = useState('select')
  const mountedRef = useRef(true)
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const fsIframeRef = useRef<HTMLIFrameElement | null>(null)
  const stripRef = useRef<HTMLDivElement | null>(null)
  const activeToolRef = useRef(activeTool)
  const canvasRef = useRef<HTMLDivElement | null>(null)
  const [canvasInset, setCanvasInset] = useState({ left: 0, right: 0 })
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
    setActiveTool('select')
  }, [deckSlug, scope, refreshSignal])

  useEffect(() => { activeToolRef.current = activeTool }, [activeTool])

  // Compute horizontal inset so the nav overlay buttons align with the 16:9
  // slide content inside the iframe (which letterboxes when the container is
  // wider than 16:9). The iframe's AstDeck.scaleToParent uses the same math.
  useEffect(() => {
    const el = canvasRef.current
    if (!el) return
    const update = () => {
      const w = el.clientWidth
      const h = el.clientHeight
      if (!w || !h) return
      const scale = Math.min(w / 1920, h / 1080)
      const inset = Math.max(0, Math.round((w - 1920 * scale) / 2))
      setCanvasInset(prev => (prev.left === inset && prev.right === inset ? prev : { left: inset, right: inset }))
    }
    update()
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

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

  // Extract custom font families from the deck theme's embedded-fonts declaration
  // so the TextFormatBar dropdown shows them alongside system fonts.
  const customFonts = useMemo(() => {
    const raw = deck?.deck.theme?.['embedded-fonts']
    if (!raw) return []
    try {
      const refs: { family?: string }[] = JSON.parse(raw)
      const seen = new Set<string>()
      return refs
        .map(r => r.family?.trim() ?? '')
        .filter(f => { if (!f || seen.has(f)) return false; seen.add(f); return true })
    } catch { return [] }
  }, [deck?.deck.theme])

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
      if (event.source !== iframeRef.current?.contentWindow) return
      const data = event.data as {
        type?: string
        index?: number
        id?: string | null
        tag?: string | null
        clickX?: number; clickY?: number
        x?: number; y?: number; w?: number; h?: number
        rotation?: number
        fill?: string; stroke?: string; strokeWidth?: number; opacity?: number
        font?: string; fontSize?: number; fontWeight?: string; align?: string; color?: string
        changes?: SlideEditDraft['moves']
        moves?: SlideEditDraft['moves']
        resizes?: SlideEditDraft['resizes']
        texts?: SlideEditDraft['texts']
        deletes?: string[]
        attrs?: { id: string; attrs: Record<string, string> }[]
        creates?: { id: string; tag: string; attrs: Record<string, string>; text?: string }[]
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
        if (!id) {
          // Canvas click with a shape tool active → create element
          const tool = activeToolRef.current
          if (tool !== 'select' && tool !== 'image') {
            const spec = SHAPE_DEFAULTS[tool]
            if (spec) {
              const cx = typeof data.clickX === 'number' ? data.clickX : 400
              const cy = typeof data.clickY === 'number' ? data.clickY : 300
              iframeRef.current?.contentWindow?.postMessage({
                type: 'ast-edit-create',
                tag: spec.tag,
                x: cx - spec.w / 2,
                y: cy - spec.h / 2,
                w: spec.w,
                h: spec.h,
                defaults: spec.defaults,
              }, '*')
              setActiveTool('select')
              return
            }
          }
          setSelectedObject(null)
        } else {
          setSelectedObject({
            id,
            tag,
            x: Number(data.x) || 0,
            y: Number(data.y) || 0,
            w: Number(data.w) || 0,
            h: Number(data.h) || 0,
            rotation: Number(data.rotation) || 0,
            fill: String(data.fill ?? ''),
            stroke: String(data.stroke ?? ''),
            strokeWidth: Number(data.strokeWidth) || 0,
            opacity: Number(data.opacity ?? 1),
            font: String(data.font ?? ''),
            fontSize: Number(data.fontSize) || 0,
            fontWeight: String(data.fontWeight ?? ''),
            align: String(data.align ?? ''),
            color: String(data.color ?? ''),
          })
        }
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
          ...(data.attrs?.length ? { attrs: data.attrs } : {}),
          ...(data.creates?.length ? { creates: data.creates } : {}),
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

  const handlePropertyChange = useCallback((key: string, value: string) => {
    postToCanvas({ type: 'ast-edit-set-attr', key, value })
  }, [postToCanvas])

  const handleZOrder = useCallback((direction: string) => {
    postToCanvas({ type: 'ast-edit-z-order', direction })
  }, [postToCanvas])

  const handleImagePick = useCallback(async (file: File) => {
    try {
      const result = await uploadSlideAsset(deckSlug, file, scope)
      // Convert the file to a data URL so the ast-image web component can
      // render the image immediately inside the cross-origin iframe. Blob URLs
      // are origin-scoped and cannot be loaded by the iframe; data URLs work
      // everywhere. The asset-ref is persisted for the present renderer to
      // resolve on reload.
      const dataUrl = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(reader.result as string)
        reader.onerror = () => reject(new Error('Failed to read file'))
        reader.readAsDataURL(file)
      })
      postToCanvas({
        type: 'ast-edit-create',
        tag: 'ast-image',
        x: 400,
        y: 200,
        w: 600,
        h: 400,
        defaults: { 'asset-ref': result.assetRef, src: dataUrl, fit: 'contain' },
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Image upload failed')
    }
  }, [deckSlug, scope, postToCanvas])

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
    window.open(slidesPresentationURL(deckSlug, scope, true), '_blank', 'noopener,noreferrer')
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

      {/* Editor layout — left toolbar + canvas + right properties panel */}
      <div className={cn('flex min-h-0 gap-0', fillHeight ? 'flex-1' : '')}>
        {/* Left shape toolbar — always visible, hidden in fullscreen */}
        {!fullscreen && (
          <ShapeToolbar activeTool={activeTool} onToolChange={setActiveTool} onImagePick={handleImagePick} />
        )}

        {/* Center: canvas + optional floating text bar */}
        <div className={cn('relative flex-1 min-h-0 min-w-0', !fillHeight && 'aspect-video')}>
          {/* Floating text format bar — above the canvas when a text element is selected */}
          {!fullscreen && selectedObject?.tag === 'AST-TEXT' && (
            <div className="absolute left-1/2 top-2 z-20 -translate-x-1/2">
              <TextFormatBar
                font={selectedObject.font}
                fontSize={selectedObject.fontSize}
                fontWeight={selectedObject.fontWeight}
                color={selectedObject.color}
                customFonts={customFonts}
                onPropertyChange={handlePropertyChange}
              />
            </div>
          )}
          <div ref={canvasRef} className="relative h-full w-full overflow-hidden rounded-lg">
            {deckFrame}
            {total === 0 && (
              <div className="absolute inset-0">
                {generatingPlaceholder}
              </div>
            )}
            {/* Edge navigation buttons — aligned with the scaled slide content */}
            {!fullscreen && total > 1 && (
              <>
                <button
                  type="button"
                  aria-label="Previous slide"
                  data-testid="slides-nav-prev"
                  onClick={() => setSlideIndex(prev => Math.max(0, prev - 1))}
                  disabled={boundedIndex === 0}
                  className="absolute top-0 z-10 flex h-full w-10 cursor-pointer items-center justify-center opacity-0 transition-opacity hover:opacity-100 disabled:cursor-default disabled:opacity-0"
                  style={{ left: canvasInset.left, background: 'linear-gradient(to right, rgba(0,0,0,0.3), transparent)' }}
                >
                  <span className="flex items-center justify-center rounded-full p-1" style={{ background: 'rgba(0,0,0,0.5)' }}>
                    <ChevronLeft size={18} className="text-white" />
                  </span>
                </button>
                <button
                  type="button"
                  aria-label="Next slide"
                  data-testid="slides-nav-next"
                  onClick={() => setSlideIndex(prev => Math.min(total - 1, prev + 1))}
                  disabled={boundedIndex >= total - 1}
                  className="absolute top-0 z-10 flex h-full w-10 cursor-pointer items-center justify-center opacity-0 transition-opacity hover:opacity-100 disabled:cursor-default disabled:opacity-0"
                  style={{ right: canvasInset.right, background: 'linear-gradient(to left, rgba(0,0,0,0.3), transparent)' }}
                >
                  <span className="flex items-center justify-center rounded-full p-1" style={{ background: 'rgba(0,0,0,0.5)' }}>
                    <ChevronRight size={18} className="text-white" />
                  </span>
                </button>
              </>
            )}
          </div>
        </div>

        {/* Right properties panel — shown when element is selected, hidden in fullscreen */}
        {!fullscreen && selectedObject && (
          <ElementPropertiesPanel
            selection={selectedObject}
            onPropertyChange={handlePropertyChange}
            onZOrder={handleZOrder}
          />
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
