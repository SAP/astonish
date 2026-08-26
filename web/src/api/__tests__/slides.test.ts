import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  exportSlidesDeck,
  fetchSlidesDeck,
  fetchSlidesPresentation,
  slidesPresentationURL,
  deckSlideThumbnailUrl,
  listSlidesDecks,
  listSlidesTemplates,
  importSlidesTemplate,
  deleteSlidesTemplate,
  duplicateSlidesTemplate,
  recolorSlidesTemplate,
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

  it('builds a scoped, encoded per-slide thumbnail URL', () => {
    expect(deckSlideThumbnailUrl('deck-1', 0, 'personal')).toBe('/api/docs/slides/deck-1/thumbnails/0?scope=personal')
    expect(deckSlideThumbnailUrl('risk/a', 3, 'team')).toBe('/api/docs/slides/risk%2Fa/thumbnails/3?scope=team')
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

describe('slides API (templates)', () => {
  beforeEach(() => mockedTeamFetch.mockReset())
  afterEach(() => vi.clearAllMocks())

  it('listSlidesTemplates GETs /api/docs/slides/templates and returns templates', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({
      templates: [{ name: 'aurora', label: 'Aurora', tokens: { surface: '#fff', ink: '#000', accent: '#0af' } }],
    }), { status: 200 }))
    const { templates } = await listSlidesTemplates()
    expect(mockedTeamFetch).toHaveBeenCalledWith('/api/docs/slides/templates')
    expect(templates).toHaveLength(1)
    expect(templates[0].name).toBe('aurora')
  })

  it('listSlidesTemplates tolerates missing templates field', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }))
    const { templates } = await listSlidesTemplates()
    expect(templates).toEqual([])
  })

  it('importSlidesTemplate POSTs multipart FormData without a JSON content-type', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ template: { name: 'imported', label: 'Imported' } }), { status: 200 }))
    const file = new File(['pptx-bytes'], 'deck.pptx', { type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation' })
    const res = await importSlidesTemplate(file)
    expect(res.template.name).toBe('imported')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/import')
    expect(init.method).toBe('POST')
    expect(init.body).toBeInstanceOf(FormData)
    expect((init.body as FormData).get('file')).toBe(file)
    // Must NOT force a JSON content-type — the browser sets the multipart boundary.
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBeNull()
  })

  it('importSlidesTemplate appends scope when provided', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ template: { name: 'x' } }), { status: 200 }))
    const file = new File(['b'], 'd.pptx')
    await importSlidesTemplate(file, 'team')
    const [, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect((init.body as FormData).get('scope')).toBe('team')
  })

  it('importSlidesTemplate throws on non-ok response', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('nope', { status: 400 }))
    await expect(importSlidesTemplate(new File(['b'], 'd.pptx'))).rejects.toThrow()
  })

  it('deleteSlidesTemplate issues DELETE with scope query', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('', { status: 200 }))
    await deleteSlidesTemplate('corp', 'team')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/templates/corp?scope=team')
    expect(init.method).toBe('DELETE')
  })

  it('deleteSlidesTemplate throws on non-ok response', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('forbidden', { status: 403 }))
    await expect(deleteSlidesTemplate('midnight')).rejects.toThrow()
  })

  it('duplicateSlidesTemplate POSTs JSON body with opts', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ template: { name: 'corp-copy', label: 'Corp (copy)' } }), { status: 200 }))
    const res = await duplicateSlidesTemplate('corp', { newName: 'corp-copy', newLabel: 'Corp (copy)' })
    expect(res.template.name).toBe('corp-copy')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/templates/corp/duplicate?scope=personal')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ newName: 'corp-copy', newLabel: 'Corp (copy)' })
  })

  it('duplicateSlidesTemplate throws on non-ok response', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('not found', { status: 404 }))
    await expect(duplicateSlidesTemplate('nope')).rejects.toThrow()
  })

  it('recolorSlidesTemplate PATCHes tokens as JSON', async () => {
    mockedTeamFetch.mockResolvedValue(new Response(JSON.stringify({ name: 'corp', tokens: { accent: '#FF8800' } }), { status: 200 }))
    await recolorSlidesTemplate('corp', { accent: '#FF8800', surface: '#101010' }, 'team')
    const [url, init] = mockedTeamFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/docs/slides/templates/corp/recolor?scope=team')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ tokens: { accent: '#FF8800', surface: '#101010' } })
  })

  it('recolorSlidesTemplate throws on non-ok response', async () => {
    mockedTeamFetch.mockResolvedValue(new Response('bad hex', { status: 400 }))
    await expect(recolorSlidesTemplate('corp', { accent: 'red' })).rejects.toThrow()
  })
})
