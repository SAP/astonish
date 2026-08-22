import { useState, useEffect, useCallback, useMemo } from 'react'
import { Presentation, Trash2, ArrowLeft, Clock, Upload, GitFork } from 'lucide-react'
import {
  listSlidesDecks,
  fetchSlidesDeck,
  deleteSlidesDeck,
  slidesPresentationURL,
  type SlidesDeckListItem,
  type SlidesSlide,
  type DocsScope,
} from '../api/slides'
import SlidesDeckView from './chat/SlidesDeckView'

interface SlidesViewProps {
  theme: string
  deckSlug?: string
  isPlatformMode?: boolean
  onNavigate?: (path: string) => void
  onPublishDeck?: (deck: SlidesDeckListItem) => void
  onForkDeck?: (deck: SlidesDeckListItem) => void
}

// Cap thumbnails per card to avoid mounting dozens of iframes.
const MAX_THUMBS = 5

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
 * A single slide thumbnail: a scaled, non-interactive sandboxed iframe pointed
 * at the deck's present document at `#slide-<n>` (DeckController resolves the
 * positional hash via data-index). Same transport as SlidesDeckView, so the
 * themed background matches. The title is rendered beneath the tile.
 */
function SlideThumbnail({ deckSlug, scope, index, title }: { deckSlug: string; scope: DocsScope; index: number; title?: string }) {
  const src = `${slidesPresentationURL(deckSlug, scope)}#slide-${index + 1}`
  return (
    <div className="flex w-40 shrink-0 flex-col gap-1">
      <div
        className="relative aspect-video w-40 overflow-hidden rounded-md border"
        style={{ borderColor: 'var(--border-color)', background: 'var(--card)' }}
      >
        <iframe
          src={src}
          sandbox="allow-scripts"
          title={`${title || `Slide ${index + 1}`} thumbnail`}
          aria-hidden="true"
          tabIndex={-1}
          loading="lazy"
          className="pointer-events-none absolute left-0 top-0 origin-top-left border-0"
          // The present doc renders at a fixed canvas; scale it down to fit the
          // 160px-wide tile. width/height are the pre-scale logical size.
          style={{ width: '640px', height: '360px', transform: 'scale(0.25)' }}
        />
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
  const [slides, setSlides] = useState<SlidesSlide[]>([])

  useEffect(() => {
    let cancelled = false
    fetchSlidesDeck(deck.slug, scope)
      .then(res => { if (!cancelled) setSlides(res.slides) })
      .catch(() => { /* thumbnails are best-effort */ })
    return () => { cancelled = true }
  }, [deck.slug, scope])

  const shown = slides.slice(0, MAX_THUMBS)
  const remaining = Math.max(0, slides.length - shown.length)

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

        {/* Thumbnail strip */}
        {shown.length > 0 ? (
          <div className="flex gap-3 overflow-x-auto pb-1">
            {shown.map((slide, i) => (
              <SlideThumbnail key={slide.id} deckSlug={deck.slug} scope={scope} index={i} title={slide.title} />
            ))}
            {remaining > 0 && (
              <div
                className="flex w-24 shrink-0 items-center justify-center rounded-md border text-xs text-muted-foreground"
                style={{ borderColor: 'var(--border-color)' }}
              >
                +{remaining} more
              </div>
            )}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            {deck.description && deck.description !== deck.title ? deck.description : 'Open to view slides'}
          </p>
        )}
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

export default function SlidesView({ theme, deckSlug, isPlatformMode, onNavigate, onPublishDeck, onForkDeck }: SlidesViewProps) {
  void theme
  const [decks, setDecks] = useState<SlidesDeckListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedSlug, setSelectedSlug] = useState<string | null>(deckSlug || null)
  const [deleteConfirm, setDeleteConfirm] = useState<{ slug: string; title: string; scope: DocsScope } | null>(null)

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
      </div>
    </div>
  )
}
