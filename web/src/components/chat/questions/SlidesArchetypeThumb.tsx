import { createElement, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties } from 'react'

import { registerSlidesRuntime } from '@/components/docs/slides/runtime'
import { listSlidesTemplates, templateMediaUrl } from '@/api/slides'

export interface SlidesArchetypeThumbProps {
  markup: string
  theme?: Record<string, string>
  /**
   * Template name whose assets resolve any asset-ref tokens in `markup`. The
   * asset map is fetched from the slides API at render time (and cached) so the
   * chat payload never embeds data: bytes.
   */
  template?: string
  /**
   * Optional pre-resolved asset map (asset-ref -> data URI). Normally omitted;
   * assets are resolved from `template` instead. Retained for callers that
   * already hold a deck's assets.
   */
  assets?: Record<string, string>
  className?: string
}

/**
 * Module-level cache of template name -> asset map so we fetch each template's
 * assets at most once across every thumbnail in the session. A single
 * ask_user select card mounts one thumbnail per variant (up to ~12), all for
 * the same template, so this collapses N fetches into one.
 */
const templateAssetsCache = new Map<string, Record<string, string>>()
const templateAssetsInflight = new Map<string, Promise<Record<string, string>>>()

async function loadTemplateAssets(template: string): Promise<Record<string, string>> {
  const cached = templateAssetsCache.get(template)
  if (cached) return cached
  const inflight = templateAssetsInflight.get(template)
  if (inflight) return inflight
  const promise = (async () => {
    try {
      const { templates } = await listSlidesTemplates()
      const match = templates.find((t) => t.name === template)
      const assets = match?.assets ?? {}
      templateAssetsCache.set(template, assets)
      return assets
    } catch {
      templateAssetsCache.set(template, {})
      return {}
    } finally {
      templateAssetsInflight.delete(template)
    }
  })()
  templateAssetsInflight.set(template, promise)
  return promise
}

type EmbeddedFontRef = { family: string; variant?: string; assetKey: string }

function declaredFonts(theme?: Record<string, string>): EmbeddedFontRef[] {
  const raw = theme?.['embedded-fonts']
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as EmbeddedFontRef[]
    return Array.isArray(parsed) ? parsed.filter((r) => r?.family && r?.assetKey) : []
  } catch {
    return []
  }
}

function fontWeight(variant?: string): string {
  const v = (variant || '').trim()
  if (v === 'bold' || v === 'boldItalic') return '700'
  if (/^\d+$/.test(v)) return v
  return '400'
}

function fontStyle(variant?: string): string {
  const v = (variant || '').trim()
  return v === 'italic' || v === 'boldItalic' ? 'italic' : 'normal'
}

function safeThumbFontFamily(family: string): boolean {
  if (!family) return false
  for (const ch of family) {
    if (ch === '"' || ch === '\\' || ch === '<' || ch === '>' || ch.charCodeAt(0) < 0x20) return false
  }
  return true
}

/** Inject @font-face for faces the theme declared. One style tag per template. */
function installDeclaredFonts(
  theme?: Record<string, string>,
  template?: string,
  assets?: Record<string, string>,
): void {
  const refs = declaredFonts(theme)
  if (refs.length === 0 || typeof document === 'undefined') return
  const id = `astonish-slides-fonts-${template || 'deck'}`
  if (document.getElementById(id)) return
  const rules: string[] = []
  for (const ref of refs) {
    if (!safeThumbFontFamily(ref.family)) continue
    const data = assets?.[ref.assetKey]
    const src = data
      ? `url(${data})`
      : template
        ? `url("${templateMediaUrl(template, ref.assetKey)}")`
        : ''
    if (!src) continue
    const format = data?.startsWith('data:font/woff2')
      ? ' format("woff2")'
      : !data
        ? ' format("woff2")'
        : ''
    rules.push(
      `@font-face{font-family:"${ref.family}";src:${src}${format};font-weight:${fontWeight(ref.variant)};font-style:${fontStyle(ref.variant)};font-display:swap}`,
    )
  }
  if (rules.length === 0) return
  const style = document.createElement('style')
  style.id = id
  style.textContent = rules.join('')
  document.head.append(style)
}

/**
 * Resolve any asset-ref tokens in the markup to their data URIs. Best-effort:
 * for each [ref, dataURI] pair we replace occurrences of the ref token in the
 * markup string. Missing refs are simply left untouched (the preview then omits
 * that image but still renders layout/text/colors).
 */
function resolveAssets(markup: string, assets?: Record<string, string>): string {
  if (!assets) return markup
  let resolved = markup
  for (const [ref, dataUri] of Object.entries(assets)) {
    if (!ref || !dataUri) continue
    resolved = resolved.split(ref).join(dataUri)
  }
  return resolved
}

/**
 * SlidesArchetypeThumb live-renders a single Astonish Slides archetype variant
 * markup as a non-interactive miniature. It reuses the exact ast-* web-component
 * runtime by mounting an <ast-deck> whose single child is the archetype's own
 * <ast-slide> (set via innerHTML). The runtime's DeckController activates slide 0
 * automatically, and AstDeck.scaleToParent() scales the fixed 1920x1080 canvas to
 * fit this component's parent (a 16:9 ThumbnailFrame). No server round-trip.
 *
 * Assets: the markup references images by asset-ref (never data: bytes in the
 * chat payload). We resolve those refs at render time from the template's asset
 * map, fetched once from the slides API and cached across thumbnails.
 *
 * IMPORTANT: the archetype markup is a full `<ast-slide>…</ast-slide>` fragment,
 * so it must be injected as the deck's innerHTML (becoming a direct child slide),
 * NOT nested inside another <ast-slide> — a nested slide is display:none and the
 * preview would render blank.
 */
export default function SlidesArchetypeThumb({
  markup,
  theme,
  template,
  assets,
  className,
}: SlidesArchetypeThumbProps) {
  const deckRef = useRef<HTMLElement | null>(null)
  const [templateAssets, setTemplateAssets] = useState<Record<string, string> | undefined>(
    assets ?? (template ? templateAssetsCache.get(template) : undefined),
  )

  // Ensure the ast-* custom elements are registered before we mount them.
  useEffect(() => {
    registerSlidesRuntime()
  }, [])

  // Lazily resolve the template's assets (once, cached) unless the caller
  // already supplied an explicit asset map.
  useEffect(() => {
    if (assets || !template) return
    let cancelled = false
    void loadTemplateAssets(template).then((resolved) => {
      if (!cancelled) setTemplateAssets(resolved)
    })
    return () => {
      cancelled = true
    }
  }, [assets, template])

  const effectiveAssets = assets ?? templateAssets

  const resolvedMarkup = useMemo(
    () => resolveAssets(markup, effectiveAssets),
    [markup, effectiveAssets],
  )

  // Inject the archetype's own <ast-slide> as the deck's direct child. The
  // DeckController (created in AstDeck.firstUpdated) marks slide 0 active.
  useEffect(() => {
    const deck = deckRef.current
    if (!deck) return
    try {
      deck.innerHTML = resolvedMarkup
      // Ensure the first slide is active even if the controller has not run yet
      // (e.g. before firstUpdated in some mount orders); the controller keeps it
      // in sync afterwards.
      const first = deck.querySelector(':scope > ast-slide')
      if (first) first.setAttribute('active', '')
    } catch (err) {
      // A malformed markup string must never crash the chat view; the option
      // simply renders without a thumbnail preview.
      console.error('SlidesArchetypeThumb failed to render markup', err)
    }
  }, [resolvedMarkup])

  // Apply theme tokens as --ast-* CSS custom properties on the deck element so
  // token-based fills/colors resolve exactly as in a real deck render.
  useEffect(() => {
    const deck = deckRef.current
    if (!deck || !theme) return
    try {
      for (const [key, value] of Object.entries(theme)) {
        if (!key || key === 'embedded-fonts' || key === 'template-name') continue
        deck.style.setProperty(`--ast-${key}`, value)
      }
    } catch {
      // Ignore invalid token values.
    }
    installDeclaredFonts(theme, template, effectiveAssets)
  }, [theme, template, effectiveAssets])

  // The wrapper is the deck's parent; AstDeck.scaleToParent() fits the 1920x1080
  // canvas to it (uniform min(w/1920, h/1080)). transform-origin is top-left in
  // the runtime styles, so anchor the deck there. pointer-events:none keeps the
  // thumbnail non-interactive.
  const wrapperStyle: CSSProperties = {
    position: 'relative',
    width: '100%',
    height: '100%',
    overflow: 'hidden',
    pointerEvents: 'none',
    userSelect: 'none',
  }

  return (
    <div className={className} style={wrapperStyle}>
      {createElement('ast-deck', {
        ref: deckRef,
        style: { position: 'absolute', top: 0, left: 0 } as CSSProperties,
      })}
    </div>
  )
}
