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
})
