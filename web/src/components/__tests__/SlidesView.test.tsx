/// <reference types="vitest" />

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import SlidesView from '../SlidesView'
import * as slidesApi from '../../api/slides'
import '@testing-library/jest-dom'

vi.mock('../../api/slides', async () => {
  const actual = await vi.importActual<typeof import('../../api/slides')>('../../api/slides')
  return {
    ...actual,
    listSlidesDecks: vi.fn(),
    fetchSlidesDeck: vi.fn(),
    deleteSlidesDeck: vi.fn(),
    slidesPresentationURL: vi.fn((slug: string) => `/api/docs/slides/${slug}/present`),
    deckSlideThumbnailUrl: vi.fn((slug: string, index: number) => `/api/docs/slides/${slug}/thumbnails/${index}`),
  }
})

// Stub the heavy deck renderer; we only assert SlidesView wiring.
vi.mock('../chat/SlidesDeckView', () => ({
  default: ({ deckSlug }: { deckSlug: string }) => <div data-testid="deck-view">{deckSlug}</div>,
}))

const personalDeck: slidesApi.SlidesDeckListItem = {
  id: 'p1',
  slug: 'personal-deck',
  title: 'Personal Deck',
  schemaVersion: 1,
  scope: 'personal',
  updatedAt: new Date().toISOString(),
  thumbnailReady: true,
}

const teamDeck: slidesApi.SlidesDeckListItem = {
  id: 't1',
  slug: 'team-deck',
  title: 'Team Deck',
  schemaVersion: 1,
  scope: 'team',
  updatedAt: new Date().toISOString(),
  thumbnailReady: true,
}

describe('SlidesView', () => {
  beforeEach(() => {
    vi.mocked(slidesApi.listSlidesDecks).mockResolvedValue({ decks: [personalDeck, teamDeck] })
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('renders Personal and Team sections with deck titles and a first-page thumbnail', async () => {
    render(<SlidesView theme="dark" isPlatformMode />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())

    // 'Personal Deck' appears twice per card (header + thumbnail caption).
    expect((await screen.findAllByText('Personal Deck')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('Team Deck').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Personal').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Team').length).toBeGreaterThan(0)

    // Each card shows exactly one first-page thumbnail captioned with the deck
    // title (no per-deck slide fetch happens anymore).
    const captions = await screen.findAllByTestId('slides-thumb-title')
    expect(captions.map(n => n.textContent)).toEqual(
      expect.arrayContaining(['Personal Deck', 'Team Deck']),
    )
  })

  it('renders a pre-baked <img> thumbnail (never a live-render iframe), falling back to a placeholder on error', async () => {
    const { container } = render(<SlidesView theme="dark" isPlatformMode />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())

    // Each card thumbnail is a static baked PNG <img>, NOT a live-render iframe.
    expect(container.querySelector('iframe')).toBeNull()
    const img = await waitFor(() => {
      const el = container.querySelector('img')
      if (!el) throw new Error('no img yet')
      return el as HTMLImageElement
    })
    expect(img.getAttribute('src')).toContain('/thumbnails/0')

    // The tile that owns this <img> (the aspect-video wrapper).
    const tile = img.closest('div') as HTMLElement

    // On a 404/onError the tile swaps to the empty placeholder icon — it must
    // NOT fall back to an iframe / live render.
    fireEvent.error(img)
    await waitFor(() => expect(tile.querySelector('img')).toBeNull())
    expect(tile.querySelector('iframe')).toBeNull()
    // The placeholder icon (an <svg>) now occupies the tile.
    expect(tile.querySelector('svg')).not.toBeNull()
  })

  it('invokes onPublishDeck for a personal card', async () => {
    const onPublishDeck = vi.fn()
    render(<SlidesView theme="dark" isPlatformMode onPublishDeck={onPublishDeck} />)
    const publishBtn = await screen.findByTitle('Publish to Team')
    fireEvent.click(publishBtn)
    expect(onPublishDeck).toHaveBeenCalledWith(expect.objectContaining({ slug: 'personal-deck' }))
  })

  it('invokes onForkDeck for a team card', async () => {
    const onForkDeck = vi.fn()
    render(<SlidesView theme="dark" isPlatformMode onForkDeck={onForkDeck} />)
    const forkBtn = await screen.findByTitle('Fork to Personal')
    fireEvent.click(forkBtn)
    expect(onForkDeck).toHaveBeenCalledWith(expect.objectContaining({ slug: 'team-deck' }))
  })

  it('opens the deck detail (SlidesDeckView) when a card is clicked', async () => {
    const onNavigate = vi.fn()
    render(<SlidesView theme="dark" onNavigate={onNavigate} />)
    // Click inside the card body (the first card is the personal deck). The
    // thumbnail caption lives inside the clickable region.
    const card = (await screen.findAllByTestId('slides-thumb-title'))[0]
    fireEvent.click(card)
    expect(onNavigate).toHaveBeenCalledWith('/slides/personal-deck')
    expect(await screen.findByTestId('deck-view')).toHaveTextContent('personal-deck')
  })

  it('shows the empty state when there are no decks', async () => {
    vi.mocked(slidesApi.listSlidesDecks).mockResolvedValue({ decks: [] })
    render(<SlidesView theme="dark" />)
    expect(await screen.findByText(/No slide decks yet/i)).toBeInTheDocument()
  })

  it('shows a Manage templates link in the header', async () => {
    render(<SlidesView theme="dark" />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())
    const link = await screen.findByTestId('manage-templates-link')
    expect(link).toBeInTheDocument()
    expect(link).toHaveTextContent('Templates')
  })

  it('navigates to the templates area when the header link is clicked', async () => {
    const onNavigate = vi.fn()
    render(<SlidesView theme="dark" onNavigate={onNavigate} />)
    const link = await screen.findByTestId('manage-templates-link')
    fireEvent.click(link)
    expect(onNavigate).toHaveBeenCalledWith('/slides/templates')
  })

  it('Create Slide button calls onCreateSlide with a prompt message', async () => {
    const onCreateSlide = vi.fn()
    render(<SlidesView theme="dark" onCreateSlide={onCreateSlide} />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())
    const btn = await screen.findByTestId('create-slide-button')
    fireEvent.click(btn)
    expect(onCreateSlide).toHaveBeenCalledTimes(1)
    expect(onCreateSlide.mock.calls[0][0]).toContain('slides skill')
  })

  it('shows Create Slide button in the empty state', async () => {
    vi.mocked(slidesApi.listSlidesDecks).mockResolvedValue({ decks: [] })
    const onCreateSlide = vi.fn()
    render(<SlidesView theme="dark" onCreateSlide={onCreateSlide} />)
    expect(await screen.findByTestId('create-slide-button-empty')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('create-slide-button-empty'))
    expect(onCreateSlide).toHaveBeenCalledTimes(1)
  })
})
