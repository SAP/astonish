import { teamFetch } from './teamContext'

const DOCS_BASE = '/api/docs'

export type DocsScope = 'personal' | 'team' | 'org' | 'platform'

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
  sessionId?: string
  version?: number
  sourceSlug?: string
}

export interface SlidesSlide {
  id: string
  deckId: string
  position: number
  title?: string
  content: string
  notes?: string
  schemaVersion: number
  /** Storage key of the pre-baked PNG thumbnail asset, if one has been rendered. */
  thumbnailRef?: string
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
  thumbnailReady?: boolean
  sessionId?: string
  version?: number
  sourceSlug?: string
}

export type SlidesExportFormat = 'pdf' | 'pptx' | 'html'

/** A named archetype (layout) provided by a slide template. In the lightweight
 * list DTO only `kind` and `label` are present (no markup). Chat authoring uses
 * fill_slides against the create_deck catalog rather than copying markup. */
export interface SlidesTemplateArchetype {
  kind: string
  title?: string
  /** Human-readable variant label. */
  label?: string
  markup?: string
  /** Two-tier tag: 'fixed' = brand chrome reproduced verbatim (fill only the
   * listed fillSlots text); 'flexible' = content adapted by type. Absent on
   * built-in templates. */
  tier?: 'fixed' | 'flexible'
  /** For fixed chrome, the ast-text ids whose text the AI may substitute. */
  fillSlots?: string[]
  /** Assets key of a pre-baked PNG thumbnail, if one was generated at import. */
  thumbnailRef?: string
}

/** Representative cover for the Templates library card (same pick as the chat
 * template picker: first title* archetype, else the first archetype). */
export interface SlidesTemplateCover {
  kind: string
  thumbnailRef?: string
  /** Live-render markup; omitted when `thumbnailRef` is set. */
  markup?: string
}

/** A slide template (design tokens + assets + archetype layouts). */
export interface SlidesTemplate {
  name: string
  label?: string
  description?: string
  tokens?: Record<string, string>
  assets?: Record<string, string>
  archetypes?: SlidesTemplateArchetype[]
  scope?: string
  /** Archetype kinds (title/section/content) available in the template. */
  archetypeKinds?: string[]
  /** Whether this template has a rich style guide for LLM content generation. */
  hasStyleGuide?: boolean
  cover?: SlidesTemplateCover
}

function withScope(path: string, scope: DocsScope): string {
  const query = new URLSearchParams({ scope })
  return `${path}?${query.toString()}`
}

async function responseError(response: Response, operation: string): Promise<Error> {
  const detail = (await response.text()).trim()
  // Detect raw HTML error pages from reverse proxies (nginx 413, 502, etc.)
  if (detail.startsWith('<') || detail.startsWith('<!')) {
    if (response.status === 413) {
      return new Error(`${operation}: file too large for upload`)
    }
    return new Error(`${operation}: HTTP ${response.status}`)
  }
  return new Error(detail ? `${operation}: ${detail}` : `${operation}: HTTP ${response.status}`)
}

export interface SlideElementMove {
  id: string
  x: number
  y: number
}

export interface SlideElementText {
  id: string
  text: string
}

export interface SlideElementResize {
  id: string
  x: number
  y: number
  w: number
  h: number
}

export interface SlideEditDraft {
  moves?: SlideElementMove[]
  resizes?: SlideElementResize[]
  texts?: SlideElementText[]
  deletes?: string[]
}

export function slideEditIsDirty(draft?: SlideEditDraft | null): boolean {
  if (!draft) return false
  return (draft.moves?.length ?? 0) > 0 || (draft.resizes?.length ?? 0) > 0 || (draft.texts?.length ?? 0) > 0 || (draft.deletes?.length ?? 0) > 0
}

/** Apply canvas object moves, text edits, and deletes to a stored slide. */
export async function patchSlideMoves(
  deckSlug: string,
  index: number,
  draft: SlideElementMove[] | SlideEditDraft,
  scope: DocsScope = 'personal',
): Promise<SlidesSlide> {
  const body: SlideEditDraft = Array.isArray(draft) ? { moves: draft } : {
    moves: draft.moves?.length ? draft.moves : undefined,
    resizes: draft.resizes?.length ? draft.resizes : undefined,
    texts: draft.texts?.length ? draft.texts : undefined,
    deletes: draft.deletes?.length ? draft.deletes : undefined,
  }
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/slides/${index}`, scope), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw await responseError(response, 'Failed to save slide layout')
  return response.json() as Promise<SlidesSlide>
}

export async function fetchSlidesDeck(deckSlug: string, scope: DocsScope = 'personal'): Promise<SlidesDeckResponse> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}`, scope))
  if (!response.ok) throw await responseError(response, 'Failed to load slide deck')
  return response.json() as Promise<SlidesDeckResponse>
}

export function slidesPresentationURL(deckSlug: string, scope: DocsScope = 'personal', presenter = false): string {
  const url = withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/present`, scope)
  return presenter ? `${url}&presenter=1` : url
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

/**
 * Build the URL for a pre-baked template archetype thumbnail served by the
 * backend at `GET /api/docs/slides/templates/<name>/thumbnails/<kind>`
 * (Content-Type image/png). `kind` is the archetype kind path param.
 */
export function templateThumbnailUrl(name: string, kind: string, cacheKey?: string, scope?: DocsScope): string {
  let url = `${DOCS_BASE}/slides/templates/${encodeURIComponent(name)}/thumbnails/${encodeURIComponent(kind)}`
  if (scope && scope !== 'personal') url = withScope(url, scope)
  if (!cacheKey) return url
  const sep = url.includes('?') ? '&' : '?'
  return `${url}${sep}v=${encodeURIComponent(cacheKey)}`
}

/**
 * Build the URL for a template image asset (example cover photo) served at
 * `GET /api/docs/slides/templates/<name>/media/<ref>`.
 */
export function templateMediaUrl(name: string, ref: string, scope?: DocsScope): string {
  const url = `${DOCS_BASE}/slides/templates/${encodeURIComponent(name)}/media/${encodeURIComponent(ref)}`
  if (scope && scope !== 'personal') return withScope(url, scope)
  return url
}

/**
 * Build the URL for a pre-baked per-slide deck thumbnail served by the backend
 * at `GET /api/docs/slides/<deckSlug>/thumbnails/<index>` (Content-Type
 * image/png). `index` is the slide Position. Returns 404 when no thumbnail has
 * been baked for that slide — callers must render an EMPTY placeholder, never a
 * live render.
 */
export function deckSlideThumbnailUrl(deckSlug: string, index: number, scope: DocsScope = 'personal', cacheKey?: string): string {
  const url = withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/thumbnails/${index}`, scope)
  return cacheKey ? `${url}&v=${encodeURIComponent(cacheKey)}` : url
}

/** Load a deck thumbnail through the authenticated, team-aware request path. */
export async function fetchDeckSlideThumbnail(
  deckSlug: string,
  index: number,
  scope: DocsScope = 'personal',
  cacheKey?: string,
): Promise<Blob> {
  const response = await teamFetch(deckSlideThumbnailUrl(deckSlug, index, scope, cacheKey))
  if (!response.ok) throw await responseError(response, 'Failed to load slide thumbnail')
  return response.blob()
}

/** List available slide templates. Omit scope for the merged catalog. */
export async function listSlidesTemplates(scope?: DocsScope): Promise<{ templates: SlidesTemplate[] }> {
  const path = scope
    ? withScope(`${DOCS_BASE}/slides/templates`, scope)
    : `${DOCS_BASE}/slides/templates`
  const response = await teamFetch(path)
  if (!response.ok) throw await responseError(response, 'Failed to list slide templates')
  const data = (await response.json()) as { templates?: SlidesTemplate[] }
  return { templates: data.templates ?? [] }
}

/**
 * Import a .pptx file as a new slide template.
 *
 * NOTE: the body is a FormData, so we intentionally pass NO Content-Type
 * header — the browser sets the correct multipart/form-data boundary
 * automatically. teamFetch only adds headers we explicitly provide (plus
 * X-Requested-With / X-Astonish-Team), so this stays multipart.
 */
export async function importSlidesTemplate(
  file: File,
  scope?: DocsScope,
): Promise<{ template: { name: string; label?: string; scope?: string } }> {
  const form = new FormData()
  form.append('file', file)
  const url = scope && scope !== 'personal'
    ? withScope(`${DOCS_BASE}/slides/import`, scope)
    : `${DOCS_BASE}/slides/import`
  const response = await teamFetch(url, {
    method: 'POST',
    body: form,
  })
  if (!response.ok) throw await responseError(response, 'Failed to import template')
  return response.json() as Promise<{ template: { name: string; label?: string; scope?: string } }>
}

/** Delete a scoped slide template (built-ins are read-only and return 403). */
export async function deleteSlidesTemplate(name: string, scope: DocsScope = 'personal'): Promise<void> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/templates/${encodeURIComponent(name)}`, scope), {
    method: 'DELETE',
  })
  if (!response.ok) throw await responseError(response, 'Failed to delete template')
}

/**
 * Duplicate a template (built-in or scoped) into a new scoped template the user
 * can then edit. Returns the created template's identity.
 */
export async function duplicateSlidesTemplate(
  name: string,
  opts: { newName?: string; newLabel?: string } = {},
  scope: DocsScope = 'personal',
): Promise<{ template: { name: string; label?: string } }> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/templates/${encodeURIComponent(name)}/duplicate`, scope), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(opts),
  })
  if (!response.ok) throw await responseError(response, 'Failed to duplicate template')
  return response.json() as Promise<{ template: { name: string; label?: string } }>
}

/**
 * Recolor a scoped template's palette tokens (surface/ink/accent). Built-ins are
 * read-only and return 403.
 */
export async function recolorSlidesTemplate(
  name: string,
  tokens: Record<string, string>,
  scope: DocsScope = 'personal',
): Promise<void> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/templates/${encodeURIComponent(name)}/recolor`, scope), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tokens }),
  })
  if (!response.ok) throw await responseError(response, 'Failed to recolor template')
}

/** Version snapshot returned by the versions endpoint. */
export interface SlidesDeckVersionSnapshot {
  id: string
  deckSlug: string
  version: number
  title: string
  snapshot: string
  createdAt: string
}

/** Save (copy) a session-scoped deck to permanent storage. */
export async function saveDeck(
  deckSlug: string,
  body: { targetSlug: string; title?: string },
  scope: DocsScope = 'personal',
): Promise<SlidesDeckResponse> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/save`, scope), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw await responseError(response, 'Failed to save deck')
  return response.json() as Promise<SlidesDeckResponse>
}

/** List version history for a saved deck. */
export async function listDeckVersions(
  deckSlug: string,
  scope: DocsScope = 'personal',
): Promise<{ versions: SlidesDeckVersionSnapshot[] }> {
  const response = await teamFetch(withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/versions`, scope))
  if (!response.ok) throw await responseError(response, 'Failed to list deck versions')
  return response.json() as Promise<{ versions: SlidesDeckVersionSnapshot[] }>
}

/** Restore a specific version of a deck. */
export async function restoreDeckVersion(
  deckSlug: string,
  version: number,
  scope: DocsScope = 'personal',
): Promise<SlidesDeckResponse> {
  const response = await teamFetch(
    withScope(`${DOCS_BASE}/slides/${encodeURIComponent(deckSlug)}/versions/${version}/restore`, scope),
    { method: 'POST' },
  )
  if (!response.ok) throw await responseError(response, 'Failed to restore deck version')
  return response.json() as Promise<SlidesDeckResponse>
}
