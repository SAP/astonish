import { describe, expect, it } from 'vitest'

import { alignToCSS, cssFontFamily } from './AstText'

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

// cssFontFamily quotes brand families that are invalid as unquoted CSS
// identifiers (e.g. `72 Brand` starts with a digit) so the browser applies the
// real embedded font instead of silently dropping the declaration and falling
// back to serif (Times). Regression guard for imported-template brand fonts.
describe('cssFontFamily', () => {
  it('quotes a leading family that starts with a digit', () => {
    expect(cssFontFamily('72 Brand, Aptos, Arial, sans-serif'))
      .toBe('"72 Brand", Aptos, Arial, sans-serif')
    expect(cssFontFamily('72 Brand Medium, Aptos, Arial, sans-serif'))
      .toBe('"72 Brand Medium", Aptos, Arial, sans-serif')
  })

  it('leaves valid unquoted families and generic keywords untouched', () => {
    expect(cssFontFamily('Aptos Display, Arial, sans-serif'))
      .toBe('Aptos Display, Arial, sans-serif')
    expect(cssFontFamily('Arial')).toBe('Arial')
    expect(cssFontFamily('sans-serif')).toBe('sans-serif')
  })

  it('preserves already-quoted families and is idempotent', () => {
    expect(cssFontFamily('"72 Brand", Aptos, sans-serif'))
      .toBe('"72 Brand", Aptos, sans-serif')
    expect(cssFontFamily(cssFontFamily('72 Brand, Arial')))
      .toBe('"72 Brand", Arial')
  })

  it('quotes single brand families without a fallback list', () => {
    expect(cssFontFamily('72 Brand')).toBe('"72 Brand"')
  })

  it('returns empty input unchanged', () => {
    expect(cssFontFamily('')).toBe('')
  })
})
