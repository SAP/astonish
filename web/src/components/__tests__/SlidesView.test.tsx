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
    listSlidesTemplates: vi.fn(),
    importSlidesTemplate: vi.fn(),
    fetchSlidesDeck: vi.fn(),
    deleteSlidesDeck: vi.fn(),
    slidesPresentationURL: vi.fn((slug: string) => `/api/docs/slides/${slug}/present`),
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
}

const teamDeck: slidesApi.SlidesDeckListItem = {
  id: 't1',
  slug: 'team-deck',
  title: 'Team Deck',
  schemaVersion: 1,
  scope: 'team',
  updatedAt: new Date().toISOString(),
}

const slidesFor = (deckId: string): slidesApi.SlidesDeckResponse => ({
  deck: { id: deckId, slug: deckId, title: 'x', schemaVersion: 1 },
  slides: [
    { id: `${deckId}-s1`, deckId, position: 0, title: 'Intro Slide', content: '', schemaVersion: 1 },
    { id: `${deckId}-s2`, deckId, position: 1, title: 'Second Slide', content: '', schemaVersion: 1 },
  ],
})

describe('SlidesView', () => {
  beforeEach(() => {
    vi.mocked(slidesApi.listSlidesDecks).mockResolvedValue({ decks: [personalDeck, teamDeck] })
    vi.mocked(slidesApi.listSlidesTemplates).mockResolvedValue({
      templates: [
        { name: 'aurora', label: 'Aurora', tokens: { surface: '#111827', ink: '#f9fafb', accent: '#38bdf8' } },
      ],
    })
    vi.mocked(slidesApi.importSlidesTemplate).mockResolvedValue({ template: { name: 'imported', label: 'Imported' } })
    vi.mocked(slidesApi.fetchSlidesDeck).mockImplementation(async (slug: string) => slidesFor(slug))
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('renders Personal and Team sections with deck titles and thumbnail captions', async () => {
    render(<SlidesView theme="dark" isPlatformMode />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())

    expect(await screen.findByText('Personal Deck')).toBeInTheDocument()
    expect(screen.getByText('Team Deck')).toBeInTheDocument()
    expect(screen.getAllByText('Personal').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Team').length).toBeGreaterThan(0)

    // Thumbnail captions render from the fetched slides.
    const captions = await screen.findAllByTestId('slides-thumb-title')
    expect(captions.length).toBeGreaterThan(0)
    expect(screen.getAllByText('Intro Slide').length).toBeGreaterThan(0)
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
    const card = await screen.findByText('Personal Deck')
    fireEvent.click(card)
    expect(onNavigate).toHaveBeenCalledWith('/slides/personal-deck')
    expect(await screen.findByTestId('deck-view')).toHaveTextContent('personal-deck')
  })

  it('shows the empty state when there are no decks', async () => {
    vi.mocked(slidesApi.listSlidesDecks).mockResolvedValue({ decks: [] })
    render(<SlidesView theme="dark" />)
    expect(await screen.findByText(/No slide decks yet/i)).toBeInTheDocument()
  })

  it('shows a Manage templates link in the header with a count', async () => {
    render(<SlidesView theme="dark" />)
    await waitFor(() => expect(slidesApi.listSlidesTemplates).toHaveBeenCalled())
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

  it('imports a .pptx template via the hidden input and refetches templates', async () => {
    render(<SlidesView theme="dark" />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())

    // The visible button triggers the hidden input.
    const importBtn = await screen.findByTitle('Import a .pptx file as a slide template')
    expect(importBtn).toBeInTheDocument()

    const input = screen.getByTestId('template-import-input') as HTMLInputElement
    const file = new File(['pptx'], 'deck.pptx', { type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation' })
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(slidesApi.importSlidesTemplate).toHaveBeenCalledWith(file))
    // Refetch after import (once on mount + once after import).
    await waitFor(() => expect(vi.mocked(slidesApi.listSlidesTemplates).mock.calls.length).toBeGreaterThanOrEqual(2))
    // Success toast appears.
    expect(await screen.findByTestId('slides-toast')).toHaveTextContent(/Imported template/i)
  })

  it('shows an error toast when import fails', async () => {
    vi.mocked(slidesApi.importSlidesTemplate).mockRejectedValue(new Error('bad file'))
    render(<SlidesView theme="dark" />)
    await waitFor(() => expect(slidesApi.listSlidesDecks).toHaveBeenCalled())
    const input = screen.getByTestId('template-import-input') as HTMLInputElement
    const file = new File(['x'], 'deck.pptx')
    fireEvent.change(input, { target: { files: [file] } })
    expect(await screen.findByTestId('slides-toast')).toHaveTextContent(/Failed to import template/i)
  })
})
