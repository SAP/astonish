import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { exportSlidesDeck, fetchSlidesPresentation, slidesPresentationURL } from '@/api/slides'
import SlidesCard from './SlidesCard'

vi.mock('@/api/slides', () => ({
  exportSlidesDeck: vi.fn(),
  fetchSlidesPresentation: vi.fn(),
  slidesPresentationURL: vi.fn(() => '/api/docs/slides/migration/present?scope=team'),
}))

const update = {
  type: 'docs_update' as const,
  docType: 'slides' as const,
  deckSlug: 'migration',
  action: 'slide_written',
  slideIndex: 2,
  totalSlides: 10,
  title: 'Microservices Migration',
  validation: { errors: 0, warnings: 1 },
  pptxCapability: { native: 9, vector: 1, raster: 0, unsupported: 0 },
}

describe('SlidesCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('open', vi.fn())
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: vi.fn(() => 'blob:slides'),
      revokeObjectURL: vi.fn(),
    })
  })

  it('shows slide progress and quality summaries', () => {
    render(<SlidesCard update={update} />)

    expect(screen.getByTestId('slides-card')).toHaveTextContent('Microservices Migration')
    expect(screen.getByTestId('slides-card')).toHaveTextContent('2 / 10')
    expect(screen.getByTestId('slides-card')).toHaveTextContent('0 errors, 1 warnings')
    expect(screen.getByTestId('slides-card')).toHaveTextContent('9 native, 0 unsupported')
  })

  it('opens the authenticated presentation blob', async () => {
    vi.mocked(fetchSlidesPresentation).mockResolvedValue(new Blob(['deck'], { type: 'text/html' }))
    render(<SlidesCard update={update} scope="team" />)

    fireEvent.click(screen.getByRole('button', { name: 'Present' }))

    await waitFor(() => expect(fetchSlidesPresentation).toHaveBeenCalledWith('migration', 'team'))
    expect(window.open).toHaveBeenCalledWith('/api/docs/slides/migration/present?scope=team', '_blank', 'noopener,noreferrer')
    expect(slidesPresentationURL).toHaveBeenCalledWith('migration', 'team')
  })

  it('reloads the presentation after an empty deck receives its first slide', async () => {
    const emptyDeckUpdate = {
      ...update,
      action: 'deck_created',
      slideIndex: undefined,
      totalSlides: 0,
    }
    const writtenSlideUpdate = {
      ...update,
      action: 'slide_written',
      slideIndex: 1,
      totalSlides: 1,
    }
    vi.mocked(fetchSlidesPresentation)
      .mockRejectedValueOnce(new Error('slides content not found'))
      .mockResolvedValueOnce(new Blob(['deck'], { type: 'text/html' }))

    const { rerender } = render(<SlidesCard update={emptyDeckUpdate} scope="team" />)

    expect(await screen.findByRole('alert')).toHaveTextContent('slides content not found')
    expect(fetchSlidesPresentation).toHaveBeenNthCalledWith(1, 'migration', 'team')

    rerender(<SlidesCard update={writtenSlideUpdate} scope="team" />)

    await waitFor(() => expect(fetchSlidesPresentation).toHaveBeenCalledTimes(2))
    expect(fetchSlidesPresentation).toHaveBeenNthCalledWith(2, 'migration', 'team')
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Present' }))

    expect(window.open).toHaveBeenCalledWith('/api/docs/slides/migration/present?scope=team', '_blank', 'noopener,noreferrer')
    expect(slidesPresentationURL).toHaveBeenCalledWith('migration', 'team')
    expect(fetchSlidesPresentation).toHaveBeenCalledTimes(2)
  })

  it('surfaces export failures', async () => {
    vi.mocked(exportSlidesDeck).mockRejectedValue(new Error('export unavailable'))
    render(<SlidesCard update={update} />)

    fireEvent.click(screen.getByRole('button', { name: 'PPTX' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('export unavailable')
  })
})
