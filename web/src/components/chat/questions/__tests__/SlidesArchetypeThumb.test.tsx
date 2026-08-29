import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'

import SlidesArchetypeThumb from '../SlidesArchetypeThumb'

// jsdom does not implement ResizeObserver; provide a minimal mock so the
// component's scaling effect can run without throwing during the smoke test.
if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver === 'undefined') {
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

describe('SlidesArchetypeThumb', () => {
  afterEach(() => {
    cleanup()
    document.getElementById('astonish-slides-fonts-modern')?.remove()
    document.getElementById('astonish-slides-fonts-aurora')?.remove()
  })

  it('mounts a slide from archetype markup without throwing', () => {
    const { container } = render(
      <SlidesArchetypeThumb markup='<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Hello</ast-text></ast-slide>' />,
    )

    const deck = container.querySelector('ast-deck')
    expect(deck).not.toBeNull()

    // The archetype's <ast-slide> is injected as the deck's direct child and
    // activated so its content is visible (not nested/hidden).
    const slide = deck?.querySelector(':scope > ast-slide')
    expect(slide).not.toBeNull()
    expect(slide?.getAttribute('active')).not.toBeNull()
    expect(deck?.textContent).toContain('Hello')
  })

  it('applies theme tokens as --ast-* custom properties', () => {
    const { container } = render(
      <SlidesArchetypeThumb
        markup='<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Hi</ast-text></ast-slide>'
        theme={{ surface: '#0b1220', ink: '#e2e8f0' }}
      />,
    )
    const deck = container.querySelector('ast-deck') as HTMLElement | null
    expect(deck).not.toBeNull()
    expect(deck?.style.getPropertyValue('--ast-surface')).toBe('#0b1220')
  })

  it('injects @font-face for faces the theme declared', () => {
    render(
      <SlidesArchetypeThumb
        markup='<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Hi</ast-text></ast-slide>'
        template="modern"
        assets={{}}
        theme={{
          displayFont: 'Manrope',
          'embedded-fonts': JSON.stringify([
            { family: 'Manrope', variant: '400', assetKey: 'font:Manrope:400' },
            { family: 'JetBrains Mono', variant: '600', assetKey: 'font:JetBrains Mono:600' },
          ]),
        }}
      />,
    )
    const style = document.getElementById('astonish-slides-fonts-modern')
    expect(style?.textContent).toContain('font-family:"Manrope"')
    expect(style?.textContent).toContain('font-family:"JetBrains Mono"')
    expect(style?.textContent).toContain('/api/docs/slides/templates/modern/media/font%3AManrope%3A400')
    expect(style?.textContent).toContain('/api/docs/slides/templates/modern/media/font%3AJetBrains%20Mono%3A600')
  })

  it('does not load fonts when the theme declares none', () => {
    render(
      <SlidesArchetypeThumb
        markup='<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Hi</ast-text></ast-slide>'
        template="aurora"
        assets={{}}
        theme={{ displayFont: 'Manrope' }}
      />,
    )
    expect(document.getElementById('astonish-slides-fonts-aurora')).toBeNull()
    expect(document.getElementById('astonish-slides-fonts-modern')).toBeNull()
  })
})
