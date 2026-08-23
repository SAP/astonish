import { describe, expect, it } from 'vitest'

import { alignToCSS } from './AstText'

// alignToCSS maps the ASD `align` attribute (l | ctr | r, plus tolerated full
// words) to a valid CSS text-align value. This mirrors the PPTX exporter's
// alignMap so HTML/PDF and PowerPoint center text the same way. Regression
// guard: a raw `ctr` must become `center`, not fall through to `left`.
describe('alignToCSS', () => {
  it('maps ASD short forms to CSS text-align', () => {
    expect(alignToCSS('ctr')).toBe('center')
    expect(alignToCSS('c')).toBe('center')
    expect(alignToCSS('l')).toBe('left')
    expect(alignToCSS('r')).toBe('right')
  })

  it('passes through CSS full words', () => {
    expect(alignToCSS('center')).toBe('center')
    expect(alignToCSS('centre')).toBe('center')
    expect(alignToCSS('left')).toBe('left')
    expect(alignToCSS('right')).toBe('right')
    expect(alignToCSS('justify')).toBe('justify')
  })

  it('defaults unknown/empty values to left', () => {
    expect(alignToCSS('')).toBe('left')
    expect(alignToCSS('bogus')).toBe('left')
  })
})
