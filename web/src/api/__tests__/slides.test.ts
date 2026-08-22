import { afterEach, describe, expect, it, vi } from 'vitest'

import { exportSlidesDeck, fetchSlidesDeck, fetchSlidesPresentation, slidesPresentationURL } from '../slides'
import { teamFetch } from '../teamContext'

vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))

const mockedTeamFetch = vi.mocked(teamFetch)

describe('slides API', () => {
  afterEach(() => vi.clearAllMocks())

  it('loads an encoded, scoped deck URL', async () => {
    const payload = { deck: { id: '1', slug: 'risk/a', title: 'Risk', schemaVersion: 1 }, slides: [] }
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify(payload), { status: 200 }))

    await expect(fetchSlidesDeck('risk/a', 'team')).resolves.toEqual(payload)
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/risk%2Fa?scope=team')
  })

  it('loads presentation HTML as a blob', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('<html></html>', {
      status: 200,
      headers: { 'Content-Type': 'text/html' },
    }))

    const result = await fetchSlidesPresentation('migration')

    expect(result.type).toBe('text/html')
    expect(slidesPresentationURL('migration')).toBe('/api/docs/slides/migration/present?scope=personal')
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/migration/present?scope=personal')
  })

  it('posts exports and reports server errors with context', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('render failed', { status: 500 }))

    await expect(exportSlidesDeck('migration', 'pptx')).rejects.toThrow('Failed to export slide deck as PPTX: render failed')
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/migration/export/pptx?scope=personal', { method: 'POST' })
  })
})
