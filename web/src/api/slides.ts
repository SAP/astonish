import { teamFetch } from './teamContext'

const DOCS_BASE = '/api/docs'

export type DocsScope = 'personal' | 'team'

export interface SlidesValidationSummary {
  errors: number
  warnings: number
}

export interface SlidesPptxCapability {
  native: number
  vector: number
  raster: number
  unsupported: number
}

export interface SlidesDeck {
  id: string
  slug: string
  title: string
  description?: string
  schemaVersion: number
  theme?: Record<string, string>
}

export interface SlidesSlide {
  id: string
  deckId: string
  position: number
  title?: string
  content: string
  notes?: string
  schemaVersion: number
}

export interface SlidesDeckResponse {
  deck: SlidesDeck
  slides: SlidesSlide[]
}

/** Summary item returned by the merged deck list (GET /api/docs). */
export interface SlidesDeckListItem {
  id: string
  slug: string
  title: string
  description?: string
  schemaVersion: number
  theme?: Record<string, string>
  scope?: DocsScope
  updatedAt?: string
  slideCount?: number
}

export type SlidesExportFormat = 'pdf' | 'pptx' | 'html'

function withScope(path: string, scope: DocsScope): string {
  const query = new URLSearchParams({ scope })
  return `${path}?${query.toString()}`
}

async function responseError(response: Response, operation: string): Promise<Error> {
  const detail = (await response.text()).trim()
  return new Error(detail ? `${operation}: ${detail}` : `${operation}: HTTP ${response.status}`)
}

export async function fetchSlidesDeck(deckSlug: string, scope: DocsScope = 'personal'): Promise<SlidesDeckResponse> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}`, scope))
  if (!response.ok) throw await responseError(response, 'Failed to load slide deck')
  return response.json() as Promise<SlidesDeckResponse>
}

export function slidesPresentationURL(deckSlug: string, scope: DocsScope = 'personal'): string {
  return withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/present`, scope)
}

export async function fetchSlidesPresentation(deckSlug: string, scope: DocsScope = 'personal'): Promise<Blob> {
  const response = await teamFetch(slidesPresentationURL(deckSlug, scope))
  if (!response.ok) throw await responseError(response, 'Failed to load slide presentation')
  return response.blob()
}

export async function exportSlidesDeck(deckSlug: string, format: SlidesExportFormat, scope: DocsScope = 'personal'): Promise<Blob> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/export/${format}`, scope), {
    method: 'POST',
  })
  if (!response.ok) throw await responseError(response, `Failed to export slide deck as ${format.toUpperCase()}`)
  return response.blob()
}

/** List all decks (personal + team merged, each annotated with scope). */
export async function listSlidesDecks(): Promise<{ decks: SlidesDeckListItem[] }> {
  const response = await teamFetch(DOCS_BASE)
  if (!response.ok) throw await responseError(response, 'Failed to list slide decks')
  const data = (await response.json()) as { type?: string; decks?: SlidesDeckListItem[] }
  return { decks: data.decks ?? [] }
}

/** Publish a personal deck to the current team. */
export async function publishDeckToTeam(slug: string): Promise<{ slug: string }> {
  const response = await teamFetch(`${DOCS_BASE}/slides/publish`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug }),
  })
  if (!response.ok) throw await responseError(response, 'Failed to publish deck')
  return response.json() as Promise<{ slug: string }>
}

/** Fork a team deck back to personal. */
export async function forkDeckToPersonal(slug: string): Promise<{ slug: string }> {
  const response = await teamFetch(`${DOCS_BASE}/slides/fork`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug, source: 'team' }),
  })
  if (!response.ok) throw await responseError(response, 'Failed to fork deck')
  return response.json() as Promise<{ slug: string }>
}

/** Delete a deck from a specific scope. */
export async function deleteSlidesDeck(deckSlug: string, scope: DocsScope = 'personal'): Promise<void> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}`, scope), {
    method: 'DELETE',
  })
  if (!response.ok) throw await responseError(response, 'Failed to delete slide deck')
}
