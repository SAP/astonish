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
