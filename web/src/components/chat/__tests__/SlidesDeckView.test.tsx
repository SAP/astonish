/// <reference types="vitest" />

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import SlidesDeckView from '../SlidesDeckView'
import * as slidesApi from '../../../api/slides'
import '@testing-library/jest-dom'

if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver === 'undefined') {
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

vi.mock('../../../api/slides', async () => {
  const actual = await vi.importActual<typeof import('../../../api/slides')>('../../../api/slides')
  return {
    ...actual,
    fetchSlidesDeck: vi.fn(),
    exportSlidesDeck: vi.fn(),
    patchSlideMoves: vi.fn(),
    slidesPresentationURL: vi.fn((slug: string) => `/api/docs/slides/${slug}/present`),
  }
})

const deckWith = (n: number): slidesApi.SlidesDeckResponse => ({
  deck: { id: 'd', slug: 'd', title: 'Deck', schemaVersion: 2, sessionId: 'sess-1' },
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
    expect(screen.getByTestId('slides-deck-frame').getAttribute('src')).toContain('t=2')
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

  it('shows the "generating" placeholder while the deck has no slides', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(0))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(slidesApi.fetchSlidesDeck).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('slides-generating')).toBeInTheDocument()
    expect(screen.getByText(/Generating slides/i)).toBeInTheDocument()
    // No slide tiles yet.
    expect(screen.queryAllByTestId('slides-tile')).toHaveLength(0)
  })

  it('navigates with arrow keys when a thumbnail is focused', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(3))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    const tiles = await screen.findAllByTestId('slides-tile')
    const frame = screen.getByTestId('slides-deck-frame') as HTMLIFrameElement
    const postMessage = vi.fn()
    Object.defineProperty(frame, 'contentWindow', { value: { postMessage }, configurable: true })

    tiles[0].focus()
    fireEvent.keyDown(tiles[0], { key: 'ArrowRight' })

    await waitFor(() => {
      expect(tiles[1]).toHaveAttribute('aria-current', 'true')
    })
    expect(postMessage).toHaveBeenCalledWith({ type: 'ast-nav', index: 1 }, '*')

    fireEvent.keyDown(tiles[1], { key: 'ArrowRight' })
    await waitFor(() => {
      expect(tiles[2]).toHaveAttribute('aria-current', 'true')
    })

    fireEvent.keyDown(tiles[2], { key: 'ArrowLeft' })
    await waitFor(() => {
      expect(tiles[1]).toHaveAttribute('aria-current', 'true')
    })
  })

  it('navigates via postMessage on thumbnail click without reloading the iframe', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(3))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getAllByTestId('slides-tile')).toHaveLength(3))

    const frame = screen.getByTestId('slides-deck-frame') as HTMLIFrameElement
    expect(frame.getAttribute('src')).toContain('t=0')
    const srcBefore = frame.src
    const postMessage = vi.fn()
    Object.defineProperty(frame, 'contentWindow', { value: { postMessage }, configurable: true })

    // Click the third thumbnail.
    fireEvent.click(screen.getAllByTestId('slides-tile')[2])

    await waitFor(() =>
      expect(postMessage).toHaveBeenCalledWith({ type: 'ast-nav', index: 2 }, '*'),
    )
    // The iframe src must NOT change on navigation (no reload / no remount).
    expect(frame.src).toBe(srcBefore)
    expect(frame).toBe(screen.getByTestId('slides-deck-frame'))
  })

  it('selects the matching strip tile when the iframe reports ast-deck-change', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(5))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getAllByTestId('slides-tile')).toHaveLength(5))

    expect(screen.getAllByTestId('slides-tile')[0]).toHaveAttribute('aria-current', 'true')

    fireEvent(window, new MessageEvent('message', { data: { type: 'ast-deck-change', index: 4 } }))

    await waitFor(() => {
      expect(screen.getAllByTestId('slides-tile')[4]).toHaveAttribute('aria-current', 'true')
    })
    expect(screen.getAllByTestId('slides-tile')[0]).not.toHaveAttribute('aria-current')
  })

  it('keeps the selected tile ring outside overflow clipping', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(3))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    const tiles = await screen.findAllByTestId('slides-tile')
    const selected = tiles[0]
    expect(selected.className).not.toMatch(/\boverflow-hidden\b/)
    expect(selected.className).toMatch(/ring-2/)
    const strip = selected.parentElement
    expect(strip?.className).toMatch(/pt-1\.5/)
    expect(strip?.className).toMatch(/px-1/)
    expect(selected.querySelector('.overflow-hidden')).not.toBeNull()
  })

  it('renders live slide markup in strip tiles when content is present', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue({
      deck: { id: 'd', slug: 'd', title: 'Deck', schemaVersion: 2, theme: { surface: '#0057D2', 'template-name': 'gco' } },
      slides: [{
        id: 'd-s0',
        deckId: 'd',
        position: 0,
        title: 'Cover',
        content: '<ast-slide id="s0"><ast-text id="h" x="40" y="40" w="400" h="80">Cover title</ast-text></ast-slide>',
        schemaVersion: 2,
      }],
    })
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getByTestId('slides-tile')).toBeInTheDocument())
    const tile = screen.getByTestId('slides-tile')
    expect(tile.querySelector('ast-deck')).not.toBeNull()
    await waitFor(() => expect(tile.textContent).toContain('Cover title'))
  })

  it('shows Discard/Apply after a canvas move and discard posts ast-edit-reset', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(2))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getAllByTestId('slides-tile')).toHaveLength(2))

    const frame = screen.getByTestId('slides-deck-frame') as HTMLIFrameElement
    const postMessage = vi.fn()
    Object.defineProperty(frame, 'contentWindow', { value: { postMessage }, configurable: true })

    expect(screen.queryByTestId('slides-edit-apply')).not.toBeInTheDocument()
    expect(screen.getByTestId('slides-save')).toBeInTheDocument()

    fireEvent(window, new MessageEvent('message', {
      data: { type: 'ast-edit-changed', index: 0, moves: [{ id: 'headline', x: 200, y: 400 }], texts: [], deletes: [] },
    }))

    expect(await screen.findByTestId('slides-edit-apply')).toBeInTheDocument()
    expect(screen.getByTestId('slides-edit-discard')).toBeInTheDocument()
    expect(screen.queryByTestId('slides-save')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('slides-edit-discard'))
    expect(postMessage).toHaveBeenCalledWith({ type: 'ast-edit-reset' }, '*')
    expect(screen.queryByTestId('slides-edit-apply')).not.toBeInTheDocument()
    expect(screen.getByTestId('slides-save')).toBeInTheDocument()
  })

  it('applies pending moves through patchSlideMoves', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(1))
    vi.mocked(slidesApi.patchSlideMoves).mockResolvedValue({
      id: 'd-s0',
      deckId: 'd',
      position: 0,
      content: '<ast-slide id="s0"></ast-slide>',
      schemaVersion: 2,
    })
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getByTestId('slides-tile')).toBeInTheDocument())

    const frame = screen.getByTestId('slides-deck-frame') as HTMLIFrameElement
    const postMessage = vi.fn()
    Object.defineProperty(frame, 'contentWindow', { value: { postMessage }, configurable: true })

    expect(screen.getByTestId('slides-save')).toBeInTheDocument()
    fireEvent(window, new MessageEvent('message', {
      data: { type: 'ast-edit-changed', index: 0, moves: [{ id: 'headline', x: 200, y: 400 }], texts: [], deletes: [] },
    }))
    expect(screen.queryByTestId('slides-save')).not.toBeInTheDocument()
    fireEvent.click(await screen.findByTestId('slides-edit-apply'))

    await waitFor(() => expect(slidesApi.patchSlideMoves).toHaveBeenCalledWith(
      'd',
      0,
      { moves: [{ id: 'headline', x: 200, y: 400 }], texts: [], deletes: [] },
      'personal',
    ))
    expect(postMessage).toHaveBeenCalledWith({ type: 'ast-edit-commit' }, '*')
    await waitFor(() => expect(screen.getByTestId('slides-save')).toBeInTheDocument())
  })

  it('shows Delete when an object is selected and posts ast-edit-delete', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(1))
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getByTestId('slides-tile')).toBeInTheDocument())

    const frame = screen.getByTestId('slides-deck-frame') as HTMLIFrameElement
    const postMessage = vi.fn()
    Object.defineProperty(frame, 'contentWindow', { value: { postMessage }, configurable: true })

    expect(screen.queryByTestId('slides-edit-delete')).not.toBeInTheDocument()
    fireEvent(window, new MessageEvent('message', {
      data: { type: 'ast-edit-selected', index: 0, id: 'headline', tag: 'AST-TEXT' },
    }))
    expect(await screen.findByTestId('slides-edit-delete')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('slides-edit-delete'))
    expect(postMessage).toHaveBeenCalledWith({ type: 'ast-edit-delete' }, '*')
  })

  it('applies pending text and deletes through patchSlideMoves', async () => {
    vi.mocked(slidesApi.fetchSlidesDeck).mockResolvedValue(deckWith(1))
    vi.mocked(slidesApi.patchSlideMoves).mockResolvedValue({
      id: 'd-s0',
      deckId: 'd',
      position: 0,
      content: '<ast-slide id="s0"></ast-slide>',
      schemaVersion: 2,
    })
    render(<SlidesDeckView deckSlug="d" refreshSignal={0} />)
    await waitFor(() => expect(screen.getByTestId('slides-tile')).toBeInTheDocument())

    fireEvent(window, new MessageEvent('message', {
      data: { type: 'ast-edit-changed', index: 0, moves: [], texts: [{ id: 'headline', text: 'Hello' }], deletes: ['dek'] },
    }))
    fireEvent.click(await screen.findByTestId('slides-edit-apply'))

    await waitFor(() => expect(slidesApi.patchSlideMoves).toHaveBeenCalledWith(
      'd',
      0,
      { moves: [], texts: [{ id: 'headline', text: 'Hello' }], deletes: ['dek'] },
      'personal',
    ))
  })
})
