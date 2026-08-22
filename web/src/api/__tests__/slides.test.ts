import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  exportSlidesDeck,
  fetchSlidesDeck,
  fetchSlidesPresentation,
  slidesPresentationURL,
  listSlidesDecks,
  publishDeckToTeam,
  forkDeckToPersonal,
  deleteSlidesDeck,
} from '../slides'
import { teamFetch } from '../teamContext'

vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))

const mockedTeamFetch = vi.mocked(teamFetch)

describe('slides API (deck/present/export)', () => {
  afterEach(() => vi.clearAllMocks())

  it('loads an encoded, scoped deck URL', async () => {
    const payload = { deck: { id: '1', slug: 'risk/a', title: 'Risk', schemaVersion: 1 }, slides: [] }
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify(payload), { status: 200 }))
    const res = await fetchSlidesDeck('risk/a', 'team')
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/risk%2Fa?scope=team')
    expect(res.deck.title).toBe('Risk')
  })

  it('builds a scoped presentation URL', () => {
    expect(slidesPresentationURL('deck-1', 'personal')).toBe('/api/docs/slides/deck-1/present?scope=personal')
  })

  it('exports a deck blob', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('binary', { status: 200 }))
    const blob = await exportSlidesDeck('deck-1', 'pdf', 'personal')
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/deck-1/export/pdf?scope=personal', { method: 'POST' })
    expect(await blob.text()).toBe('binary')
  })

  it('fetches a presentation blob', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('html', { status: 200 }))
    const blob = await fetchSlidesPresentation('deck-1', 'personal')
    expect(await blob.text()).toBe('html')
  })
})

describe('slides API (list/publish/fork/delete)', () => {
  beforeEach(() => mockedTeamFetch.mockReset())
  afterEach(() => vi.clearAllMocks())

  it('listSlidesDecks hits GET /api/docs and returns decks', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ type: 'slides', decks: [{ slug: 'a', title: 'A', scope: 'personal' }] }), { status: 200 }))
    const { decks } = await listSlidesDecks()
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs')
    expect(decks).toHaveLength(1)
    expect(decks[0].slug).toBe('a')
  })

  it('listSlidesDecks tolerates missing decks field', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ type: 'slides' }), { status: 200 }))
    const { decks } = await listSlidesDecks()
    expect(decks).toEqual([])
  })

  it('publishDeckToTeam POSTs slug to /api/docs/slides/publish', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ slug: 'a' }), { status: 200 }))
    const res = await publishDeckToTeam('a')
    expect(res.slug).toBe('a')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/publish')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ slug: 'a' })
  })

  it('forkDeckToPersonal POSTs slug + source team to /api/docs/slides/fork', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ slug: 'a' }), { status: 200 }))
    await forkDeckToPersonal('a')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/fork')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ slug: 'a', source: 'team' })
  })

  it('deleteSlidesDeck issues DELETE with scope query', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('', { status: 200 }))
    await deleteSlidesDeck('a', 'team')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/a?scope=team')
    expect(init.method).toBe('DELETE')
  })

  it('throws on non-ok list response', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('boom', { status: 500 }))
    await expect(listSlidesDecks()).rejects.toThrow()
  })
})
