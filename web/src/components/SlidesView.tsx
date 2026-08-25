import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { Presentation, Trash2, ArrowLeft, Clock, Upload, GitFork, FileUp, Layers } from 'lucide-react'
import {
  listSlidesDecks,
  listSlidesTemplates,
  importSlidesTemplate,
  deleteSlidesDeck,
  deckSlideThumbnailUrl,
  type SlidesDeckListItem,
  type SlidesTemplate,
  type DocsScope,
} from '../api/slides'
import SlidesDeckView from './chat/SlidesDeckView'
import TemplatesArea from './slides/TemplatesArea'

interface SlidesViewProps {
  theme: string
  deckSlug?: string
  templatesView?: boolean
  isPlatformMode?: boolean
  onNavigate?: (path: string) => void
  onPublishDeck?: (deck: SlidesDeckListItem) => void
  onForkDeck?: (deck: SlidesDeckListItem) => void
}

function EmptyState() {
  return (
    <div className="flex flex-1 items-center justify-center">
      <div className="text-center">
        <Presentation size={48} className="mx-auto mb-4 text-[color:var(--success)]/30" />
        <h2 className="mb-2 text-lg font-semibold text-foreground">Slides</h2>
        <p className="max-w-md text-sm text-muted-foreground">
          No slide decks yet. Ask in Chat to build a presentation, then it appears here.
        </p>
      </div>
    </div>
  )
}

function formatDate(dateStr?: string) {
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

/**
 * A single slide thumbnail: a pre-baked static PNG served by the backend at
 * `GET /api/docs/slides/<slug>/thumbnails/<index>`. Baked once when a deck is
 * finished (the model's review_deck step), so opening the Slides view no longer
 * boots the full slides runtime per tile. When no thumbnail has been baked (or
 * the fetch 404s), we show an EMPTY placeholder icon — we NEVER fall back to a
 * live ast-deck render here.
 *
 * The image is LAZY: it only mounts once the tile scrolls into view (via
 * IntersectionObserver); off-screen cards render the lightweight icon
 * placeholder until the user scrolls to them.
 */
function SlideThumbnail({ deckSlug, scope, index, title }: { deckSlug: string; scope: DocsScope; index: number; title?: string }) {
  const src = deckSlideThumbnailUrl(deckSlug, index, scope)
  const tileRef = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  const [imgFailed, setImgFailed] = useState(false)

  useEffect(() => {
    // No IntersectionObserver (jsdom/tests, very old browsers): render eagerly.
    if (typeof IntersectionObserver === 'undefined') {
      setVisible(true)
      return
    }
    const node = tileRef.current
    if (!node) return
    const observer = new IntersectionObserver(
      entries => {
        if (entries.some(e => e.isIntersecting)) {
          setVisible(true)
          observer.disconnect()
        }
      },
      { rootMargin: '200px' },
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  return (
    <div className="flex w-40 shrink-0 flex-col gap-1">
      <div
        ref={tileRef}
        className="relative aspect-video w-40 overflow-hidden rounded-md border"
        style={{ borderColor: 'var(--border-color)', background: 'var(--card)' }}
      >
        {visible && !imgFailed ? (
          <img
            src={src}
            alt={title || `Slide ${index + 1}`}
            aria-hidden="true"
            loading="lazy"
            onError={() => setImgFailed(true)}
            className="absolute inset-0 h-full w-full object-cover"
          />
        ) : (
          <div className="absolute inset-0 flex items-center justify-center" aria-hidden="true">
            <Presentation size={20} style={{ color: '#10b981' }} />
          </div>
        )}
      </div>
      <span className="line-clamp-2 text-[10px] leading-tight text-muted-foreground" data-testid="slides-thumb-title">
        {title || `Slide ${index + 1}`}
      </span>
    </div>
  )
}

function DeckCard({
  deck,
  isPlatformMode,
  onOpen,
  onPublish,
  onFork,
  onDelete,
}: {
  deck: SlidesDeckListItem
  isPlatformMode?: boolean
  onOpen: (deck: SlidesDeckListItem) => void
  onPublish?: (deck: SlidesDeckListItem) => void
  onFork?: (deck: SlidesDeckListItem) => void
  onDelete: (deck: SlidesDeckListItem) => void
}) {
  const scope: DocsScope = deck.scope ?? 'personal'

  return (
    <div
      className="group flex flex-col overflow-hidden rounded-xl transition-all hover:shadow-lg"
      style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
      data-testid="slides-card"
    >
      <div className="flex-1 cursor-pointer p-4" onClick={() => onOpen(deck)}>
        <div className="mb-3 flex items-start justify-between">
          <div className="flex min-w-0 items-center gap-2">
            <Presentation size={16} style={{ color: '#10b981', flexShrink: 0 }} />
            <span className="truncate text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              {deck.title}
            </span>
          </div>
          {isPlatformMode && deck.scope && (
            <span
              className="ml-2 shrink-0 rounded-full px-1.5 py-0.5 text-[10px]"
              style={{
                background: scope === 'personal' ? 'rgba(99, 102, 241, 0.15)' : 'rgba(16, 185, 129, 0.15)',
                color: scope === 'personal' ? '#818cf8' : '#34d399',
              }}
            >
              {scope === 'personal' ? 'Personal' : 'Team'}
            </span>
          )}
        </div>

        {/* Single first-page thumbnail (lazy-mounted when scrolled into view). */}
        <div className="flex gap-3">
          <SlideThumbnail deckSlug={deck.slug} scope={scope} index={0} title={deck.title} />
        </div>
      </div>

      <div
        className="mt-auto flex items-center justify-between px-4 pb-3 pt-2"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        <div className="flex items-center gap-1 text-[10px]" style={{ color: 'var(--text-muted)' }}>
          <Clock size={10} />
          <span>{formatDate(deck.updatedAt)}</span>
        </div>
        <div className="flex items-center gap-2" onClick={e => e.stopPropagation()}>
          <div className="flex gap-1 opacity-0 transition-opacity group-hover:opacity-100">
            {isPlatformMode && scope === 'personal' && onPublish && (
              <button
                onClick={e => { e.stopPropagation(); onPublish(deck) }}
                className="rounded p-1 transition-all hover:bg-blue-500/20"
                title="Publish to Team"
              >
                <Upload size={12} className="text-blue-400" />
              </button>
            )}
            {isPlatformMode && scope === 'team' && onFork && (
              <button
                onClick={e => { e.stopPropagation(); onFork(deck) }}
                className="rounded p-1 transition-all hover:bg-green-500/20"
                title="Fork to Personal"
              >
                <GitFork size={12} className="text-green-400" />
              </button>
            )}
            <button
              onClick={e => { e.stopPropagation(); onDelete(deck) }}
              className="cursor-pointer rounded p-1 transition-colors"
              style={{ color: 'var(--text-muted)' }}
              title="Delete deck"
            >
              <Trash2 size={12} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

/** Small swatch row for a template's core tokens. */
export function TemplateSwatches({ tokens }: { tokens?: Record<string, string> }) {
  const keys = ['surface', 'ink', 'accent'] as const
  const colors = keys
    .map(k => tokens?.[k])
    .filter((c): c is string => Boolean(c))
  if (colors.length === 0) return null
  return (
    <div className="flex items-center gap-0.5" data-testid="template-swatches">
      {colors.map((c, i) => (
        <div
          key={i}
          className="h-3 w-3 rounded-sm border"
          style={{ backgroundColor: c, borderColor: 'var(--border-color)' }}
        />
      ))}
    </div>
  )
}

export default function SlidesView({ theme, deckSlug, templatesView, isPlatformMode, onNavigate, onPublishDeck, onForkDeck }: SlidesViewProps) {
  void theme
  const [decks, setDecks] = useState<SlidesDeckListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedSlug, setSelectedSlug] = useState<string | null>(deckSlug || null)
  const [deleteConfirm, setDeleteConfirm] = useState<{ slug: string; title: string; scope: DocsScope } | null>(null)
  const [templates, setTemplates] = useState<SlidesTemplate[]>([])
  const [importing, setImporting] = useState(false)
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' } | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const showToast = useCallback((message: string, type: 'success' | 'error') => {
    setToast({ message, type })
    setTimeout(() => setToast(null), 4000)
  }, [])

  const loadTemplates = useCallback(async () => {
    try {
      const data = await listSlidesTemplates()
      setTemplates(data.templates || [])
    } catch {
      // Templates are best-effort; ignore load failures.
    }
  }, [])

  useEffect(() => { loadTemplates() }, [loadTemplates])

  const handleImportClick = useCallback(() => {
    fileInputRef.current?.click()
  }, [])

  const handleImportFile = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // Reset so the same file can be re-selected.
    if (!file) return
    setImporting(true)
    try {
      const { template } = await importSlidesTemplate(file)
      await loadTemplates()
      window.dispatchEvent(new CustomEvent('astonish:slides-updated'))
      showToast(`Imported template "${template.label || template.name}"`, 'success')
    } catch (err) {
      showToast(`Failed to import template: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    } finally {
      setImporting(false)
    }
  }, [loadTemplates, showToast])

  const loadDecks = useCallback(async () => {
    try {
      const data = await listSlidesDecks()
      setDecks(data.decks || [])
    } catch {
      // Ignore errors
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadDecks() }, [loadDecks])

  // Refresh when a deck is published/forked from anywhere (mirrors apps-updated).
  useEffect(() => {
    const handler = () => loadDecks()
    window.addEventListener('astonish:slides-updated', handler)
    return () => window.removeEventListener('astonish:slides-updated', handler)
  }, [loadDecks])

  // Deep link / back-nav sync.
  useEffect(() => {
    setSelectedSlug(deckSlug || null)
  }, [deckSlug])

  const selectedDeck = useMemo(
    () => decks.find(d => d.slug === selectedSlug) || null,
    [decks, selectedSlug]
  )

  const handleOpen = useCallback((deck: SlidesDeckListItem) => {
    setSelectedSlug(deck.slug)
    onNavigate?.(`/slides/${encodeURIComponent(deck.slug)}`)
  }, [onNavigate])

  const handleDelete = useCallback(async (slug: string, scope: DocsScope) => {
    try {
      await deleteSlidesDeck(slug, scope)
      setDecks(prev => prev.filter(d => !(d.slug === slug && (d.scope ?? 'personal') === scope)))
      if (selectedSlug === slug) {
        setSelectedSlug(null)
        onNavigate?.('/slides')
      }
    } catch {
      // Ignore
    }
    setDeleteConfirm(null)
  }, [selectedSlug, onNavigate])

  const personalDecks = isPlatformMode ? decks.filter(d => (d.scope ?? 'personal') === 'personal') : decks
  const teamDecks = isPlatformMode ? decks.filter(d => d.scope === 'team') : []
  const hasDecks = personalDecks.length > 0 || teamDecks.length > 0

  // Dedicated Templates management area (deep-linked at #/slides/templates).
  if (templatesView) {
    return (
      <>
        <TemplatesArea onNavigate={onNavigate} showToast={showToast} />
        {toast && (
          <div className="fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-3 text-sm shadow-lg"
            style={{
              background: 'var(--bg-secondary)',
              border: `1px solid ${toast.type === 'error' ? '#ef4444' : 'var(--border-color)'}`,
              color: toast.type === 'error' ? '#f87171' : 'var(--text-primary)',
            }}
            role="status"
            data-testid="slides-toast"
          >
            {toast.message}
          </div>
        )}
      </>
    )
  }

  // Detail view — reuse the existing SlidesDeckView renderer.
  if (selectedSlug) {
    const scope: DocsScope = selectedDeck?.scope ?? 'personal'
    return (
      <div className="flex flex-1 flex-col overflow-hidden" style={{ background: 'var(--bg-primary)' }}>
        <div
          className="flex shrink-0 items-center gap-3 px-4 py-2"
          style={{ borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}
        >
          <button
            onClick={() => { setSelectedSlug(null); onNavigate?.('/slides') }}
            className="flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-xs transition-colors"
            style={{ color: 'var(--text-secondary)' }}
          >
            <ArrowLeft size={14} />
            Back
          </button>
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <Presentation size={16} style={{ color: '#10b981', flexShrink: 0 }} />
            <span className="truncate text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              {selectedDeck?.title || selectedSlug}
            </span>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <SlidesDeckView deckSlug={selectedSlug} scope={scope} fillHeight />
        </div>
      </div>
    )
  }

  if (loading) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <span className="text-sm" style={{ color: 'var(--text-muted)' }}>Loading decks...</span>
      </div>
    )
  }

  if (!hasDecks) {
    return <EmptyState />
  }

  const renderCard = (deck: SlidesDeckListItem) => (
    <DeckCard
      key={`${deck.scope || 'local'}-${deck.slug}`}
      deck={deck}
      isPlatformMode={isPlatformMode}
      onOpen={handleOpen}
      onPublish={onPublishDeck}
      onFork={onForkDeck}
      onDelete={d => setDeleteConfirm({ slug: d.slug, title: d.title, scope: d.scope ?? 'personal' })}
    />
  )

  return (
    <div className="flex-1 overflow-auto p-6" style={{ background: 'var(--bg-primary)' }}>
      <div className="mx-auto max-w-5xl">
        <div className="mb-6 flex items-center gap-3">
          <Presentation size={20} style={{ color: '#10b981' }} />
          <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Slides</h1>
          <span className="rounded-full px-2 py-0.5 text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
            {decks.length}
          </span>
          <button
            onClick={() => onNavigate?.('/slides/templates')}
            className="ml-auto flex cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs transition-colors"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
            title="Manage slide templates"
            data-testid="manage-templates-link"
          >
            <Layers size={14} />
            Templates
            {templates.length > 0 && (
              <span className="rounded-full px-1 text-[10px]" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                {templates.length}
              </span>
            )}
          </button>
          <button
            onClick={handleImportClick}
            disabled={importing}
            className="flex cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs transition-colors disabled:cursor-default disabled:opacity-60"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
            title="Import a .pptx file as a slide template"
          >
            <FileUp size={14} />
            {importing ? 'Importing…' : 'Import .pptx template'}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".pptx"
            onChange={handleImportFile}
            className="hidden"
            data-testid="template-import-input"
          />
        </div>

        {isPlatformMode && (personalDecks.length > 0 || teamDecks.length > 0) ? (
          <>
            {personalDecks.length > 0 && (
              <>
                <div className="mb-3 flex items-center gap-2">
                  <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Personal</span>
                  <span className="rounded-full px-1.5 py-0.5 text-[10px]" style={{ background: 'rgba(99, 102, 241, 0.15)', color: '#818cf8' }}>
                    {personalDecks.length}
                  </span>
                </div>
                <div className="mb-6 grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                  {personalDecks.map(renderCard)}
                </div>
              </>
            )}
            {teamDecks.length > 0 && (
              <>
                <div className="mb-3 flex items-center gap-2">
                  <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Team</span>
                  <span className="rounded-full px-1.5 py-0.5 text-[10px]" style={{ background: 'rgba(16, 185, 129, 0.15)', color: '#34d399' }}>
                    {teamDecks.length}
                  </span>
                </div>
                <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                  {teamDecks.map(renderCard)}
                </div>
              </>
            )}
          </>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
            {decks.map(renderCard)}
          </div>
        )}

        {deleteConfirm && (
          <div className="fixed inset-0 z-50 flex items-center justify-center" style={{ background: 'rgba(0,0,0,0.5)' }}>
            <div className="mx-4 w-full max-w-sm rounded-xl p-6" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
              <h3 className="mb-2 text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Delete Deck</h3>
              <p className="mb-4 text-xs" style={{ color: 'var(--text-muted)' }}>
                Are you sure you want to delete <strong>{deleteConfirm.title}</strong>? This cannot be undone.
              </p>
              <div className="flex justify-end gap-2">
                <button
                  onClick={() => setDeleteConfirm(null)}
                  className="cursor-pointer rounded px-3 py-1.5 text-xs"
                  style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
                >
                  Cancel
                </button>
                <button
                  onClick={() => handleDelete(deleteConfirm.slug, deleteConfirm.scope)}
                  className="cursor-pointer rounded px-3 py-1.5 text-xs text-white"
                  style={{ background: '#ef4444' }}
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        )}

        {toast && (
          <div className="fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-3 text-sm shadow-lg"
            style={{
              background: 'var(--bg-secondary)',
              border: `1px solid ${toast.type === 'error' ? '#ef4444' : 'var(--border-color)'}`,
              color: toast.type === 'error' ? '#f87171' : 'var(--text-primary)',
            }}
            role="status"
            data-testid="slides-toast"
          >
            {toast.message}
          </div>
        )}
      </div>
    </div>
  )
}
