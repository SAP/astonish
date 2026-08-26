import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import QuestionOptionThumb from '../QuestionOptionThumb'

// jsdom does not implement ResizeObserver; provide a minimal mock so the
// live SlidesArchetypeThumb fallback can run without throwing.
if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver === 'undefined') {
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

describe('QuestionOptionThumb', () => {
  afterEach(() => {
    cleanup()
  })

  it('renders an <img> from the baked thumbnail endpoint for kind "image"', () => {
    render(
      <QuestionOptionThumb
        thumbnail={{ kind: 'image', template: 'aurora', assetRef: 'thumb/title' }}
        label="Title layout"
      />,
    )

    const img = screen.getByAltText('Title layout') as HTMLImageElement
    expect(img.tagName).toBe('IMG')
    expect(img.getAttribute('src')).toContain('/slides/templates/')
    expect(img.getAttribute('src')).toContain('/thumbnails/')
    // assetRef `thumb/title` maps to the `title` kind path param.
    expect(img.getAttribute('src')).toContain('/thumbnails/title')
    expect(img.getAttribute('src')).toContain('aurora')
  })

  it('falls back to the live archetype thumb when the image errors and markup exists', () => {
    const { container } = render(
      <QuestionOptionThumb
        thumbnail={{
          kind: 'image',
          template: 'aurora',
          assetRef: 'thumb/title',
          markup: '<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Fallback</ast-text></ast-slide>',
        }}
        label="Title layout"
      />,
    )

    const img = screen.getByAltText('Title layout')
    fireEvent.error(img)

    // After the error, the <img> is swapped for the live rendered deck.
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('ast-deck')).not.toBeNull()
    expect(container.querySelector('ast-deck')?.textContent).toContain('Fallback')
  })

  it('live-renders markup for kind "slides-archetype" (built-in path)', () => {
    const { container } = render(
      <QuestionOptionThumb
        thumbnail={{
          kind: 'slides-archetype',
          markup: '<ast-slide id="s0"><ast-text id="h" x="0" y="0" w="1920" h="200">Built-in</ast-text></ast-slide>',
        }}
        label="Built-in layout"
      />,
    )

    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('ast-deck')).not.toBeNull()
    expect(container.querySelector('ast-deck')?.textContent).toContain('Built-in')
  })

  it('renders nothing when neither an image nor markup is available', () => {
    const { container } = render(
      <QuestionOptionThumb thumbnail={{ kind: 'slides-archetype' }} label="Empty" />,
    )
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('ast-deck')).toBeNull()
  })
})
