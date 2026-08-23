/// <reference types="vitest" />

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import SlidesDeckView from '../SlidesDeckView'
import * as slidesApi from '../../../api/slides'
import '@testing-library/jest-dom'

vi.mock('../../../api/slides', async () => {
  const actual = await vi.importActual<typeof import('../../../api/slides')>('../../../api/slides')
  return {
    ...actual,
    fetchSlidesDeck: vi.fn(),
    exportSlidesDeck: vi.fn(),
    slidesPresentationURL: vi.fn((slug: string) => `/api/docs/slides/${slug}/present`),
  }
})

const deckWith = (n: number): slidesApi.SlidesDeckResponse => ({
  deck: { id: 'd', slug: 'd', title: 'Deck', schemaVersion: 2 },
  slides: Array.from({ length: n }, (_, i) => ({
    id: `d-s${i}`,
    deckId: 'd',
    position: i,
    title: `Slide ${i + 1}`,
    content: '',
    schemaVersion: 2,
  })),
})

describe('SlidesDeckView refresh signal', () => {
  beforeEach(() => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(1))
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('fetches the deck once on initial render', async () => {
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(1))
    expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledWith('d', 'personal')
  })

  it('re-fetches the deck when refreshSignal bumps', async () => {
    const { rerender } = render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(1))

    rerender(<SlidesDeckView deckSlug="d" refreshSignal={1} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(2))

    rerender(<SlidesDeckView deckSlug="d" refreshSignal={2} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(3))
  })

  it('renders newly arrived slides after the deck grows from 0 to N', async () => {
    // First load: empty deck (placeholder state, no slide tiles).
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValueOnce(deckWith(0))
    const { rerender } = render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(1))
    expect(screen.queryAllByTestId('slides-tile')).toHaveLength(0)

    // A slide arrives: bump the signal, deck now resolves with slides.
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValueOnce(deckWith(3))
    rerender(<SlidesDeckView deckSlug="d" refreshSignal={1} />)
    await waitFor(() => expect(screen.getAllByTestId('slides-tile')).toHaveLength(3))

    // The embedded present iframe is still present (re-mounted, not crashed).
    expect(screen.getByTestId('slides-deck-frame')).toBeInTheDocument()
  })
})
